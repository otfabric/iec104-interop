// SPDX-License-Identifier: GPL-3.0-or-later
package org.otfabric.iec104interop;

import java.io.IOException;
import java.time.Instant;
import java.time.format.DateTimeParseException;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import java.util.function.BooleanSupplier;

import javax.net.SocketFactory;

import org.openmuc.j60870.ASdu;
import org.openmuc.j60870.ASduType;
import org.openmuc.j60870.CauseOfTransmission;
import org.openmuc.j60870.ClientConnectionBuilder;
import org.openmuc.j60870.Connection;
import org.openmuc.j60870.ConnectionEventListener;
import org.openmuc.j60870.ie.IeAckFileOrSectionQualifier;
import org.openmuc.j60870.ie.IeChecksum;
import org.openmuc.j60870.ie.IeDoubleCommand;
import org.openmuc.j60870.ie.IeDoubleCommand.DoubleCommandState;
import org.openmuc.j60870.ie.IeFileReadyQualifier;
import org.openmuc.j60870.ie.IeFileSegment;
import org.openmuc.j60870.ie.IeLastSectionOrSegmentQualifier;
import org.openmuc.j60870.ie.IeLengthOfFileOrSection;
import org.openmuc.j60870.ie.IeNameOfFile;
import org.openmuc.j60870.ie.IeNameOfSection;
import org.openmuc.j60870.ie.IeNormalizedValue;
import org.openmuc.j60870.ie.IeQualifierOfCounterInterrogation;
import org.openmuc.j60870.ie.IeQualifierOfInterrogation;
import org.openmuc.j60870.ie.IeQualifierOfSetPointCommand;
import org.openmuc.j60870.ie.IeRegulatingStepCommand;
import org.openmuc.j60870.ie.IeRegulatingStepCommand.StepCommandState;
import org.openmuc.j60870.ie.IeScaledValue;
import org.openmuc.j60870.ie.IeSectionReadyQualifier;
import org.openmuc.j60870.ie.IeSelectAndCallQualifier;
import org.openmuc.j60870.ie.IeShortFloat;
import org.openmuc.j60870.ie.IeSingleCommand;
import org.openmuc.j60870.ie.IeStatusOfFile;
import org.openmuc.j60870.ie.IeTestSequenceCounter;
import org.openmuc.j60870.ie.IeTime56;
import org.openmuc.j60870.ie.InformationElement;
import org.openmuc.j60870.ie.InformationObject;

import com.google.gson.JsonArray;
import com.google.gson.JsonNull;
import com.google.gson.JsonObject;

/** The controlling station of the contract: one bounded operation per invocation. */
final class ClientCommand implements ConnectionEventListener {
    /** A failed operation: becomes the error object of the result. */
    private static final class Failure extends Exception {
        private static final long serialVersionUID = 1L;
        final String code;

        Failure(String code, String message) {
            super(message);
            this.code = code;
        }
    }

    // Shared with the receive thread; guarded by this.
    private final JsonArray asdus;
    private final JsonArray confirmations = new JsonArray();
    private int asduCount;
    private int spontaneous;
    private boolean closed;
    private boolean terminated;
    private int negativeCot;
    private boolean readAnswered;
    private ASduType expectType;
    private int expectIoa;
    private boolean expectRead;
    /** File transfer ASDUs, for the file operation. */
    private final java.util.ArrayDeque<ASdu> fileEvents = new java.util.ArrayDeque<>();

    /** asdus: where to record what arrives; the two connections of the redundancy operation share it. */
    private ClientCommand(JsonArray asdus) {
        this.asdus = asdus;
    }

    @Override
    public synchronized void newASdu(Connection connection, ASdu asdu) {
        synchronized (asdus) {
            asdus.add(Codec.asduJson(asdu));
        }
        asduCount++;
        ASduType type = asdu.getTypeIdentification();
        if (type != null && type.getId() >= 120 && type.getId() <= 126) {
            fileEvents.add(asdu);
        }
        int cot = asdu.getCauseOfTransmission().getId();
        if (cot == 3) {
            spontaneous++;
        }
        if (expectType != null && type == expectType) {
            boolean unknown = cot >= 44 && cot <= 47;
            if (cot == 7 || cot == 9 || unknown) {
                boolean negative = asdu.isNegativeConfirm() || unknown;
                JsonObject c = new JsonObject();
                c.addProperty("cot", cot);
                c.addProperty("negative", negative);
                confirmations.add(c);
                if (negative) {
                    negativeCot = cot;
                }
            } else if (cot == 10) {
                terminated = true;
            }
        } else if (expectRead && cot == 5 && asdu.getInformationObjects() != null) {
            for (InformationObject io : asdu.getInformationObjects()) {
                int n = io.getInformationElements().length;
                if (expectIoa >= io.getInformationObjectAddress() && expectIoa < io.getInformationObjectAddress() + n) {
                    readAnswered = true;
                }
            }
        }
        notifyAll();
    }

    @Override
    public synchronized void connectionClosed(Connection connection, IOException cause) {
        if (cause != null && cause.getCause() != null) {
            Main.log("connection closed: " + cause.getCause());
        }
        closed = true;
        notifyAll();
    }

    @Override
    public void dataTransferStateChanged(Connection connection, boolean stopped) {
        // startDataTransfer and stopDataTransfer report the confirmations themselves.
    }

    /** Waits until cond holds, the connection closes or the timeout passes. */
    private synchronized boolean waitFor(BooleanSupplier cond, int timeoutMs) {
        long deadline = System.nanoTime() + timeoutMs * 1_000_000L;
        while (!cond.getAsBoolean() && !closed) {
            long left = (deadline - System.nanoTime()) / 1_000_000L;
            if (left <= 0) {
                break;
            }
            try {
                wait(left);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                break;
            }
        }
        return cond.getAsBoolean();
    }

    private synchronized boolean isClosed() {
        return closed;
    }

    private synchronized int negativeCot() {
        return negativeCot;
    }

    private synchronized void expect(ASduType type) {
        expectType = type;
    }

    private void awaitConfirmation(int n, int timeoutMs) throws Failure {
        if (!waitFor(() -> confirmations.size() >= n, timeoutMs)) {
            if (isClosed()) {
                throw new Failure("connection-lost", "connection closed while waiting for confirmation " + n);
            }
            throw new Failure("timeout", "no confirmation " + n + " from the station");
        }
        if (negativeCot() != 0) {
            throw new Failure("negative-confirmation",
                    "the station refused the request (cause " + negativeCot() + ")");
        }
    }

    private void awaitTermination(int timeoutMs) throws Failure {
        if (!waitFor(() -> terminated || negativeCot != 0, timeoutMs)) {
            if (isClosed()) {
                throw new Failure("connection-lost", "connection closed while waiting for termination");
            }
            throw new Failure("timeout", "no activation termination from the station");
        }
    }

    /** What a download produced, for the result document. */
    private static final class Download {
        final java.io.ByteArrayOutputStream data = new java.io.ByteArrayOutputStream();
        int length;
        int sections;
    }

    private ASdu nextFileEvent(int timeoutMs, String what) throws Failure {
        if (!waitFor(() -> !fileEvents.isEmpty(), timeoutMs)) {
            if (isClosed()) {
                throw new Failure("connection-lost", "connection closed during the file transfer");
            }
            throw new Failure("timeout", "the station did not send " + what);
        }
        synchronized (this) {
            return fileEvents.poll();
        }
    }

    /** Select, call, receive and acknowledge one file. See docs/CONTAINER_CONTRACT.md. */
    private void download(Connection c, int ca, int ioa, int nof, int timeoutMs, Download d)
            throws Failure, IOException {
        org.openmuc.j60870.ie.IeNameOfFile name = new org.openmuc.j60870.ie.IeNameOfFile(nof);
        CauseOfTransmission ft = CauseOfTransmission.FILE_TRANSFER;
        c.callOrSelectFiles(ca, ft, ioa, name, new org.openmuc.j60870.ie.IeNameOfSection(0),
                new org.openmuc.j60870.ie.IeSelectAndCallQualifier(1, 0));
        ASdu a = nextFileEvent(timeoutMs, "file ready");
        org.openmuc.j60870.ie.InformationElement[] e = a.getInformationObjects()[0].getInformationElements()[0];
        // A refusal is the mirrored call with an "unknown ..." cause, or a
        // file ready with the negative bit.
        boolean refused = a.getTypeIdentification() == ASduType.F_SC_NA_1 || a.isNegativeConfirm()
                || (a.getTypeIdentification() == ASduType.F_FR_NA_1
                        && ((org.openmuc.j60870.ie.IeFileReadyQualifier) e[2]).isNegativeConfirm());
        if (refused) {
            synchronized (this) {
                negativeCot = a.getCauseOfTransmission().getId();
            }
            throw new Failure("negative-confirmation",
                    "the station refused the file (cause " + a.getCauseOfTransmission().getId() + ")");
        }
        if (a.getTypeIdentification() != ASduType.F_FR_NA_1) {
            throw new Failure("failed", "expected file ready, got " + a.getTypeIdentification());
        }
        d.length = ((org.openmuc.j60870.ie.IeLengthOfFileOrSection) e[1]).getValue();

        c.callOrSelectFiles(ca, ft, ioa, name, new org.openmuc.j60870.ie.IeNameOfSection(0),
                new org.openmuc.j60870.ie.IeSelectAndCallQualifier(2, 0));
        while (true) {
            a = nextFileEvent(timeoutMs, "section ready or last section");
            e = a.getInformationObjects()[0].getInformationElements()[0];
            if (a.getTypeIdentification() == ASduType.F_LS_NA_1) {
                int lsq = ((org.openmuc.j60870.ie.IeLastSectionOrSegmentQualifier) e[2]).getValue();
                if (lsq != 1 && lsq != 2) {
                    throw new Failure("failed", "expected last section, got last segment");
                }
                // Last section: the checksum covers the whole file.
                byte[] all = d.data.toByteArray();
                boolean good = Codec.checksum(all, 0, all.length) == ((org.openmuc.j60870.ie.IeChecksum) e[3]).getValue()
                        && all.length == d.length;
                c.ackFileOrSection(ca, ioa, name, (org.openmuc.j60870.ie.IeNameOfSection) e[1],
                        new org.openmuc.j60870.ie.IeAckFileOrSectionQualifier(good ? 1 : 2, 0));
                if (!good) {
                    throw new Failure("failed", "file checksum or length mismatch after " + all.length + " octets");
                }
                return;
            }
            if (a.getTypeIdentification() != ASduType.F_SR_NA_1) {
                throw new Failure("failed", "expected section ready, got " + a.getTypeIdentification());
            }
            if (((org.openmuc.j60870.ie.IeSectionReadyQualifier) e[3]).isSectionNotReady()) {
                throw new Failure("failed", "the section is not ready");
            }
            org.openmuc.j60870.ie.IeNameOfSection nos = (org.openmuc.j60870.ie.IeNameOfSection) e[1];
            int sectionLength = ((org.openmuc.j60870.ie.IeLengthOfFileOrSection) e[2]).getValue();
            int sectionStart = d.data.size();
            d.sections++;
            c.callOrSelectFiles(ca, ft, ioa, name, nos, new org.openmuc.j60870.ie.IeSelectAndCallQualifier(6, 0));
            while (true) {
                a = nextFileEvent(timeoutMs, "a segment or last segment");
                e = a.getInformationObjects()[0].getInformationElements()[0];
                if (a.getTypeIdentification() == ASduType.F_SG_NA_1) {
                    byte[] seg = ((org.openmuc.j60870.ie.IeFileSegment) e[2]).getSegment();
                    if (d.data.size() + seg.length > d.length) {
                        throw new Failure("failed", "more data than the announced " + d.length + " octets");
                    }
                    d.data.write(seg, 0, seg.length);
                    continue;
                }
                if (a.getTypeIdentification() != ASduType.F_LS_NA_1) {
                    throw new Failure("failed", "expected a segment or last segment, got " + a.getTypeIdentification());
                }
                byte[] all = d.data.toByteArray();
                boolean good = Codec.checksum(all, sectionStart, all.length) == ((org.openmuc.j60870.ie.IeChecksum) e[3])
                        .getValue() && all.length - sectionStart == sectionLength;
                c.ackFileOrSection(ca, ioa, name, nos,
                        new org.openmuc.j60870.ie.IeAckFileOrSectionQualifier(good ? 3 : 4, 0));
                if (!good) {
                    throw new Failure("failed", "checksum or length mismatch in section " + nos.getValue());
                }
                break;
            }
        }
    }

    /** The reply to a step of an upload or to the call of the directory; a refusal ends the operation. */
    private ASdu fileReply(int timeoutMs, String what) throws Failure {
        ASdu a = nextFileEvent(timeoutMs, what);
        int cot = a.getCauseOfTransmission().getId();
        if (a.isNegativeConfirm() || (cot >= 44 && cot <= 47)) {
            throw new Failure("negative-confirmation", "the station refused (" + a.getTypeIdentification() + ", cause "
                    + cot + ")");
        }
        return a;
    }

    private void awaitCall(int scq, int nos, int timeoutMs) throws Failure {
        String what = "F_SC_NA_1 with SCQ " + scq;
        ASdu a = fileReply(timeoutMs, what);
        if (a.getTypeIdentification() != ASduType.F_SC_NA_1) {
            throw new Failure("failed", "expected " + what + ", got " + a.getTypeIdentification());
        }
        InformationElement[] e = a.getInformationObjects()[0].getInformationElements()[0];
        if (((IeSelectAndCallQualifier) e[2]).getRequest() != scq || ((IeNameOfSection) e[1]).getValue() != nos) {
            throw new Failure("failed", "expected " + what + " and NOS " + nos);
        }
    }

    private void awaitAck(int afq, int timeoutMs) throws Failure {
        String what = "F_AF_NA_1 with AFQ " + afq;
        ASdu a = fileReply(timeoutMs, what);
        if (a.getTypeIdentification() != ASduType.F_AF_NA_1) {
            throw new Failure("failed", "expected " + what + ", got " + a.getTypeIdentification());
        }
        InformationElement[] e = a.getInformationObjects()[0].getInformationElements()[0];
        int got = ((IeAckFileOrSectionQualifier) e[2]).getRequest();
        if (got == 2 || got == 4) {
            throw new Failure("negative-confirmation", "the station did not acknowledge (AFQ " + got + ")");
        }
        if (got != afq) {
            throw new Failure("failed", "expected " + what + ", got AFQ " + got);
        }
    }

    /** Announce one file and send it as the station calls it. See docs/CONTAINER_CONTRACT.md. */
    private void upload(Connection c, int ca, int ioa, int nof, int size, int sectionSize, int timeoutMs, Download d)
            throws Failure, IOException {
        byte[] content = Codec.fixtureContent(ioa, size);
        IeNameOfFile name = new IeNameOfFile(nof);
        d.length = size;
        c.fileReady(ca, ioa, name, new IeLengthOfFileOrSection(size), new IeFileReadyQualifier(0, false));
        awaitCall(2, 0, timeoutMs);
        int section = 0;
        for (int from = 0; from < size; from += sectionSize) {
            int to = Math.min(from + sectionSize, size);
            section++;
            IeNameOfSection nos = new IeNameOfSection(section);
            c.sectionReady(ca, ioa, name, nos, new IeLengthOfFileOrSection(to - from),
                    new IeSectionReadyQualifier(0, false));
            awaitCall(6, section, timeoutMs);
            d.sections++;
            d.data.write(content, from, to - from);
            for (int off = from; off < to; off += FileStation.MAX_SEGMENT) {
                c.sendSegment(ca, ioa, name, nos,
                        new IeFileSegment(content, off, Math.min(FileStation.MAX_SEGMENT, to - off)));
            }
            c.lastSectionOrSegment(ca, ioa, name, nos, new IeLastSectionOrSegmentQualifier(3),
                    new IeChecksum(Codec.checksum(content, from, to)));
            awaitAck(3, timeoutMs);
        }
        c.lastSectionOrSegment(ca, ioa, name, new IeNameOfSection(section), new IeLastSectionOrSegmentQualifier(1),
                new IeChecksum(Codec.checksum(content, 0, size)));
        awaitAck(1, timeoutMs);
    }

    /** Calls the directory and collects it up to the entry with the LFD bit. */
    private void directory(Connection c, int ca, int timeoutMs, JsonArray out) throws Failure, IOException {
        c.callOrSelectFiles(ca, CauseOfTransmission.REQUEST, 0, new IeNameOfFile(0), new IeNameOfSection(0),
                new IeSelectAndCallQualifier(0, 0));
        boolean last = false;
        while (!last) {
            ASdu a = fileReply(timeoutMs, "the entry of the directory with the LFD bit");
            if (a.getTypeIdentification() != ASduType.F_DR_TA_1) {
                throw new Failure("failed", "expected F_DR_TA_1, got " + a.getTypeIdentification());
            }
            for (InformationObject io : a.getInformationObjects()) {
                InformationElement[][] rows = io.getInformationElements();
                for (int i = 0; i < rows.length; i++) {
                    int sof = Codec.statusOctet((IeStatusOfFile) rows[i][2]);
                    JsonObject entry = new JsonObject();
                    entry.addProperty("ioa", io.getInformationObjectAddress() + i);
                    entry.addProperty("nof", ((IeNameOfFile) rows[i][0]).getValue());
                    entry.addProperty("lof", ((IeLengthOfFileOrSection) rows[i][1]).getValue());
                    entry.addProperty("sof", sof);
                    out.add(entry);
                    last |= (sof & Codec.LAST_FILE_OF_DIRECTORY) != 0;
                }
            }
        }
    }

    private void interrogateStation(Connection c, int ca, int timeoutMs) throws Failure, IOException {
        expect(ASduType.C_IC_NA_1);
        c.interrogation(ca, CauseOfTransmission.ACTIVATION, new IeQualifierOfInterrogation(20));
        awaitConfirmation(1, timeoutMs);
        awaitTermination(timeoutMs);
    }

    /** What the redundancy operation found, for the result document. */
    private static final class Redundancy {
        final boolean takeover;
        ApduTap primaryTap;
        ApduTap standbyTap;
        ClientCommand standby;
        Connection standbyConnection;
        /** Takeover: the I frames of the primary when the standby's STARTDT con arrived; before that, -1. */
        int primaryEnd = -1;
        boolean secondInterrogation;
        boolean queuedEvent;

        Redundancy(boolean takeover) {
            this.takeover = takeover;
        }

        int primaryAsdus() {
            return primaryTap == null ? 0 : primaryTap.startedFrames(primaryEnd);
        }

        int standbyAsdus() {
            return standbyTap == null ? 0 : standbyTap.startedFrames(-1);
        }

        int strays() {
            int all = (primaryTap == null ? 0 : primaryTap.frames()) + (standbyTap == null ? 0 : standbyTap.frames());
            return all - primaryAsdus() - standbyAsdus();
        }
    }

    /** Stops data transfer on a connection, as a step of the redundancy operation. */
    private void stop(ExecutorService exec, Connection c, int timeoutMs, String which) throws Failure, IOException {
        boolean confirmed;
        try {
            confirmed = uFrame(exec, c::stopDataTransfer, timeoutMs);
        } catch (IOException | RuntimeException e) {
            confirmed = false;
        }
        if (isClosed()) {
            throw new Failure("connection-lost", "the " + which + " closed");
        }
        if (!confirmed) {
            throw new Failure("stopdt-timeout", "no STOPDT con on the " + which);
        }
    }

    /** Steps 2 to 7 of the redundancy operation, on the primary: see docs/CONTAINER_CONTRACT.md. */
    private void redundancy(ExecutorService exec, Connection c, Redundancy rd, int ca, boolean tls, int timeoutMs)
            throws Failure, IOException {
        ClientCommand standby = rd.standby;
        interrogateStation(c, ca, timeoutMs);
        if (!rd.takeover) {
            stop(exec, c, timeoutMs, "primary");
        }
        start(exec, rd.standbyConnection, standby, tls, timeoutMs);
        if (rd.takeover) {
            rd.primaryEnd = rd.primaryTap.frames();
        } else if (isClosed()) {
            throw new Failure("connection-lost", "the primary closed");
        }
        if (!rd.takeover) {
            if (!standby.waitFor(() -> standby.spontaneous > 0, timeoutMs)) {
                if (standby.isClosed()) {
                    throw new Failure("connection-lost", "the standby closed while waiting for the queued event");
                }
                throw new Failure("timeout", "no event with cause 3 on the standby");
            }
            rd.queuedEvent = true;
        }
        rd.secondInterrogation = true;
        standby.interrogateStation(rd.standbyConnection, ca, timeoutMs);
        standby.stop(exec, rd.standbyConnection, timeoutMs, "standby");
        if (rd.strays() > 0) {
            throw new Failure("failed", rd.strays() + " I frames on a connection outside the time it was started");
        }
    }

    private static boolean parseBool(String s) {
        switch (s.toLowerCase()) {
        case "true":
        case "1":
        case "on":
            return true;
        case "false":
        case "0":
        case "off":
            return false;
        default:
            throw new NumberFormatException(s);
        }
    }

    /** A process command of the contract, parsed once and sent with either state of the select bit. */
    private static final class ProcessCommand {
        ASduType type;
        int ioa;
        int qualifier;
        boolean bool;
        int integer;
        float real;

        void send(Connection c, CauseOfTransmission cot, int ca, boolean select, IeTime56 time) throws IOException {
            IeQualifierOfSetPointCommand qos = new IeQualifierOfSetPointCommand(qualifier, select);
            switch (type) {
            case C_SC_NA_1: {
                IeSingleCommand v = new IeSingleCommand(bool, qualifier, select);
                if (time == null) {
                    c.singleCommand(ca, cot, ioa, v);
                } else {
                    c.singleCommandWithTimeTag(ca, cot, ioa, v, time);
                }
                break;
            }
            case C_DC_NA_1: {
                IeDoubleCommand v = new IeDoubleCommand(DoubleCommandState.getInstance(integer), qualifier, select);
                if (time == null) {
                    c.doubleCommand(ca, cot, ioa, v);
                } else {
                    c.doubleCommandWithTimeTag(ca, cot, ioa, v, time);
                }
                break;
            }
            case C_RC_NA_1: {
                IeRegulatingStepCommand v = new IeRegulatingStepCommand(StepCommandState.getInstance(integer),
                        qualifier, select);
                if (time == null) {
                    c.regulatingStepCommand(ca, cot, ioa, v);
                } else {
                    c.regulatingStepCommandWithTimeTag(ca, cot, ioa, v, time);
                }
                break;
            }
            case C_SE_NA_1:
                if (time == null) {
                    c.setNormalizedValueCommand(ca, cot, ioa, new IeNormalizedValue(integer), qos);
                } else {
                    c.setNormalizedValueCommandWithTimeTag(ca, cot, ioa, new IeNormalizedValue(integer), qos, time);
                }
                break;
            case C_SE_NB_1:
                if (time == null) {
                    c.setScaledValueCommand(ca, cot, ioa, new IeScaledValue(integer), qos);
                } else {
                    c.setScaledValueCommandWithTimeTag(ca, cot, ioa, new IeScaledValue(integer), qos, time);
                }
                break;
            default: // C_SE_NC_1
                if (time == null) {
                    c.setShortFloatCommand(ca, cot, ioa, new IeShortFloat(real), qos);
                } else {
                    c.setShortFloatCommandWithTimeTag(ca, cot, ioa, new IeShortFloat(real), qos, time);
                }
                break;
            }
        }
    }

    private static ProcessCommand parseCommand(Args args) {
        ProcessCommand cmd = new ProcessCommand();
        String typeArg = args.string("--type", "");
        String value = args.string("--value", null);
        cmd.ioa = args.integer("--ioa", 0);
        cmd.qualifier = args.integer("--qualifier", 0);
        try {
            cmd.type = ASduType.valueOf(typeArg);
        } catch (IllegalArgumentException e) {
            cmd.type = null;
        }
        if (cmd.type == null || !Fixture.COMMAND_TARGET.containsKey(cmd.type) || cmd.ioa < 1 || value == null) {
            throw new Args.UsageException("command: --type (C_SC_NA_1, C_DC_NA_1, C_RC_NA_1, C_SE_NA_1, C_SE_NB_1, "
                    + "C_SE_NC_1), --ioa and --value are required");
        }
        try {
            switch (cmd.type) {
            case C_SC_NA_1:
                cmd.bool = parseBool(value);
                break;
            case C_DC_NA_1:
            case C_RC_NA_1:
                cmd.integer = Integer.parseInt(value);
                if (cmd.integer < 0 || cmd.integer > 3) {
                    throw new NumberFormatException(value);
                }
                break;
            case C_SE_NA_1:
            case C_SE_NB_1:
                cmd.integer = Integer.parseInt(value);
                if (cmd.integer < -32768 || cmd.integer > 32767) {
                    throw new NumberFormatException(value);
                }
                break;
            default:
                cmd.real = Float.parseFloat(value);
                break;
            }
        } catch (NumberFormatException e) {
            throw new Args.UsageException("command: --value '" + value + "' is not valid for " + typeArg);
        }
        return cmd;
    }

    static int run(String op, Args args) {
        String host = args.string("--host", null);
        int port = args.integer("--port", 2404);
        int ca = args.integer("--common-address", 1);
        int oa = args.integer("--originator-address", 0);
        int timeoutMs = args.integer("--timeout-ms", 5000);
        int connectTimeoutMs = args.integer("--connect-timeout-ms", 5000);
        int collectMs = args.integer("--collect-ms", 0);
        if (host == null) {
            throw new Args.UsageException("client: --host is required");
        }

        // Operation arguments are parsed before connecting so that a usage
        // error never touches the network.
        int qoi = 20;
        int qcc = 5;
        int ioa = 0;
        int holdMs = 0;
        int durationMs = 1000;
        int maxAsdus = 0;
        long clockMs = System.currentTimeMillis();
        String mode = "direct";
        boolean withTime = false;
        ProcessCommand command = null;
        int fileName = 1;
        int fileSize = 0;
        int sectionSize = 0;
        Download download = new Download();
        JsonArray directory = new JsonArray();
        Redundancy redundancy = null;
        switch (op) {
        case "connect":
            holdMs = args.integer("--hold-ms", 0);
            break;
        case "interrogate":
            qoi = args.integer("--qoi", 20);
            break;
        case "counter-interrogate":
            qcc = args.integer("--qcc", 5);
            break;
        case "read":
            ioa = args.integer("--ioa", 0);
            if (ioa < 1) {
                throw new Args.UsageException("read: --ioa is required");
            }
            break;
        case "clock-sync": {
            String t = args.string("--time", null);
            if (t != null) {
                try {
                    clockMs = Instant.parse(t).toEpochMilli();
                } catch (DateTimeParseException e) {
                    throw new Args.UsageException("clock-sync: --time must look like 2026-01-02T03:04:05.678Z");
                }
            }
            break;
        }
        case "test-command":
            break;
        case "command":
            command = parseCommand(args);
            mode = args.string("--mode", "direct");
            withTime = args.flag("--with-time");
            if (!mode.equals("direct") && !mode.equals("select") && !mode.equals("sbo") && !mode.equals("cancel")) {
                throw new Args.UsageException("command: --mode must be direct, select, sbo or cancel");
            }
            break;
        case "file-get":
            ioa = args.integer("--ioa", 0);
            fileName = args.integer("--name", 1);
            if (ioa < 1 || fileName < 1 || fileName > 65535) {
                throw new Args.UsageException("file-get: --ioa is required; --name is 1..65535");
            }
            break;
        case "file-put":
            ioa = args.integer("--ioa", 0);
            fileName = args.integer("--name", 1);
            fileSize = args.integer("--size", 0);
            sectionSize = args.integer("--section-size", fileSize);
            if (ioa < 1 || fileName < 1 || fileName > 65535 || fileSize < 1 || fileSize > 65536 || sectionSize < 1
                    || sectionSize > fileSize || (fileSize + sectionSize - 1) / sectionSize > 255) {
                throw new Args.UsageException("file-put: --ioa and --size (1..65536) are required; --name is 1..65535;"
                        + " --section-size is 1..--size, in at most 255 sections");
            }
            break;
        case "file-dir":
            break;
        case "redundancy": {
            String switchover = args.string("--switchover", "stopdt");
            if (!switchover.equals("stopdt") && !switchover.equals("takeover")) {
                throw new Args.UsageException("redundancy: --switchover must be stopdt or takeover");
            }
            redundancy = new Redundancy(switchover.equals("takeover"));
            break;
        }
        case "monitor":
            durationMs = args.integer("--duration-ms", 1000);
            maxAsdus = args.integer("--max-asdus", 0);
            break;
        default:
            throw new Args.UsageException("client: unknown operation " + op);
        }
        Args.Apci apci = args.apci();
        Tls tls = Tls.fromArgs(args);
        args.finish("client " + op);

        ClientCommand state = new ClientCommand(new JsonArray());
        long begin = System.currentTimeMillis();
        boolean connected = false;
        boolean startdt = false;
        boolean stopdt = false;
        Failure failure = null;
        int exitCode = Main.EXIT_OPERATION_FAILED;
        Connection con = null;
        ExecutorService exec = Executors.newSingleThreadExecutor(r -> {
            Thread t = new Thread(r, "u-frame");
            t.setDaemon(true);
            return t;
        });

        try {
            try {
                ClientConnectionBuilder builder = new ClientConnectionBuilder(host)
                        .setPort(port)
                        .setConnectionTimeout(connectTimeoutMs)
                        .setMaxNumOfOutstandingIPdus(apci.k)
                        .setMaxUnconfirmedIPdusReceived(apci.w)
                        .setMaxTimeNoAckReceived(apci.t1 * 1000)
                        .setMaxTimeNoAckSent(apci.t2 * 1000)
                        .setMaxIdleTime(apci.t3 * 1000)
                        .setConnectionEventListener(state);
                SocketFactory transport = tls != null ? tls.clientFactory() : SocketFactory.getDefault();
                builder.setSocketFactory(transport);
                if (redundancy != null) {
                    redundancy.primaryTap = new ApduTap(transport);
                    redundancy.standbyTap = new ApduTap(transport);
                    redundancy.standby = new ClientCommand(state.asdus);
                    builder.setSocketFactory(redundancy.primaryTap);
                }
                con = builder.build();
                if (redundancy != null) {
                    redundancy.standbyConnection = builder.setSocketFactory(redundancy.standbyTap)
                            .setConnectionEventListener(redundancy.standby)
                            .build();
                    redundancy.standbyConnection.setOriginatorAddress(oa);
                }
            } catch (IOException e) {
                exitCode = Main.EXIT_CONNECT_FAILED;
                throw new Failure("connect-failed", "cannot connect to the station: " + e.getMessage());
            }
            connected = true;
            con.setOriginatorAddress(oa);

            // startDataTransfer waits for the confirmation for up to t1; bound it by --timeout-ms instead.
            final Connection c = con;
            start(exec, c, state, tls != null, timeoutMs);
            startdt = true;

            final int fQoi = qoi;
            final int fIoa = ioa;
            final int fMax = maxAsdus;
            IeTime56 now = Codec.time(System.currentTimeMillis());
            switch (op) {
            case "connect":
                if (holdMs > 0) {
                    state.waitFor(() -> false, holdMs);
                }
                if (!uFrame(exec, c::stopDataTransfer, timeoutMs)) {
                    throw new Failure("stopdt-timeout", "no STOPDT con from the station");
                }
                stopdt = true;
                break;
            case "interrogate":
                state.expect(ASduType.C_IC_NA_1);
                c.interrogation(ca, CauseOfTransmission.ACTIVATION, new IeQualifierOfInterrogation(fQoi));
                state.awaitConfirmation(1, timeoutMs);
                state.awaitTermination(timeoutMs);
                break;
            case "counter-interrogate":
                state.expect(ASduType.C_CI_NA_1);
                c.counterInterrogation(ca, CauseOfTransmission.ACTIVATION,
                        new IeQualifierOfCounterInterrogation(qcc & 0x3F, (qcc >> 6) & 0x03));
                state.awaitConfirmation(1, timeoutMs);
                state.awaitTermination(timeoutMs);
                break;
            case "read":
                synchronized (state) {
                    state.expectType = ASduType.C_RD_NA_1;
                    state.expectRead = true;
                    state.expectIoa = fIoa;
                }
                c.readCommand(ca, fIoa);
                if (!state.waitFor(() -> state.readAnswered || state.negativeCot != 0, timeoutMs)) {
                    throw new Failure("timeout", "no answer to the read command");
                }
                if (state.negativeCot() != 0) {
                    throw new Failure("negative-confirmation",
                            "the station refused the request (cause " + state.negativeCot() + ")");
                }
                break;
            case "clock-sync":
                state.expect(ASduType.C_CS_NA_1);
                c.synchronizeClocks(ca, Codec.time(clockMs));
                state.awaitConfirmation(1, timeoutMs);
                break;
            case "test-command":
                state.expect(ASduType.C_TS_TA_1);
                c.testCommandWithTimeTag(ca, new IeTestSequenceCounter(0x4938), now);
                state.awaitConfirmation(1, timeoutMs);
                break;
            case "command": {
                IeTime56 ts = withTime ? now : null;
                state.expect(withTime ? Codec.timeTagged(command.type) : command.type);
                int n = 0;
                if (!mode.equals("direct")) {
                    command.send(c, CauseOfTransmission.ACTIVATION, ca, true, ts);
                    state.awaitConfirmation(++n, timeoutMs);
                    if (mode.equals("cancel")) {
                        command.send(c, CauseOfTransmission.DEACTIVATION, ca, true, ts);
                        state.awaitConfirmation(++n, timeoutMs);
                    }
                }
                if (mode.equals("direct") || mode.equals("sbo")) {
                    command.send(c, CauseOfTransmission.ACTIVATION, ca, false, ts);
                    state.awaitConfirmation(++n, timeoutMs);
                    state.awaitTermination(timeoutMs);
                }
                break;
            }
            case "file-get":
                state.download(c, ca, fIoa, fileName, timeoutMs, download);
                break;
            case "file-put":
                state.upload(c, ca, fIoa, fileName, fileSize, sectionSize, timeoutMs, download);
                break;
            case "file-dir":
                state.directory(c, ca, timeoutMs, directory);
                break;
            case "redundancy":
                state.redundancy(exec, c, redundancy, ca, tls != null, timeoutMs);
                stopdt = true;
                break;
            default: // monitor
                state.waitFor(() -> fMax > 0 && state.asduCount >= fMax, durationMs);
                if (state.isClosed()) {
                    throw new Failure("connection-lost", "connection closed while monitoring");
                }
                break;
            }
            // Optionally keep listening for what the station sends afterwards.
            if (collectMs > 0) {
                state.waitFor(() -> false, collectMs);
            }
            exitCode = Main.EXIT_OK;
        } catch (Failure f) {
            failure = f;
        } catch (IOException | RuntimeException e) {
            failure = new Failure(state.isClosed() ? "connection-lost" : "failed", String.valueOf(e.getMessage()));
        } finally {
            close(con);
            if (redundancy != null) {
                close(redundancy.standbyConnection);
            }
            exec.shutdownNow();
        }

        JsonObject r = new JsonObject();
        r.addProperty("schemaVersion", Main.SCHEMA_VERSION);
        r.addProperty("adapter", Main.ADAPTER);
        r.addProperty("operation", op);
        r.addProperty("ok", failure == null);
        if (failure == null) {
            r.add("error", JsonNull.INSTANCE);
        } else {
            JsonObject e = new JsonObject();
            e.addProperty("code", failure.code);
            e.addProperty("message", failure.getMessage());
            r.add("error", e);
        }
        r.addProperty("connected", connected);
        r.addProperty("startdtConfirmed", startdt);
        if (tls != null) {
            r.addProperty("tls", startdt);
        }
        if (op.equals("connect") || op.equals("redundancy")) {
            r.addProperty("stopdtConfirmed", stopdt);
        }
        if (redundancy != null) {
            JsonObject d = new JsonObject();
            d.addProperty("switchover", redundancy.takeover ? "takeover" : "stopdt");
            d.addProperty("primaryAsdus", redundancy.primaryAsdus());
            d.addProperty("standbyAsdus", redundancy.standbyAsdus());
            d.addProperty("strayAsdus", redundancy.strays());
            d.addProperty("queuedEvent", redundancy.queuedEvent);
            r.add("redundancy", d);
        }
        if (op.equals("file-dir")) {
            r.add("directory", directory);
        }
        if (op.equals("file-get") || op.equals("file-put")) {
            byte[] data = download.data.toByteArray();
            JsonObject f = new JsonObject();
            f.addProperty("ioa", ioa);
            f.addProperty("name", fileName);
            f.addProperty("length", download.length);
            f.addProperty("received", data.length);
            f.addProperty("sections", download.sections);
            f.addProperty("sha256", Codec.sha256Hex(data));
            r.add("file", f);
        }
        JsonArray confirmations;
        boolean terminated;
        synchronized (state) {
            confirmations = state.confirmations.deepCopy();
            terminated = state.terminated;
        }
        if (redundancy != null && redundancy.standby != null) {
            synchronized (redundancy.standby) {
                confirmations.addAll(redundancy.standby.confirmations);
                if (redundancy.secondInterrogation) {
                    terminated = redundancy.standby.terminated;
                }
            }
        }
        r.add("confirmations", confirmations);
        r.addProperty("terminated", terminated);
        synchronized (state.asdus) {
            r.add("asdus", state.asdus.deepCopy());
        }
        r.addProperty("elapsedMs", System.currentTimeMillis() - begin);
        Main.emit(r);
        return exitCode;
    }

    /**
     * Sends STARTDT and waits for the confirmation. With TLS the handshake is part of this (see Tls.clientFactory), and
     * everything that goes wrong in it is the one error the contract has for it.
     */
    private static void start(ExecutorService exec, Connection c, ClientCommand state, boolean tls, int timeoutMs)
            throws Failure, IOException {
        boolean confirmed;
        try {
            confirmed = uFrame(exec, c::startDataTransfer, timeoutMs, () -> tls && state.isClosed());
        } catch (IOException | RuntimeException e) {
            if (tls) {
                throw new Failure("tls-failed", "no TLS session with the station: " + e);
            }
            throw e;
        }
        if (!confirmed) {
            throw tls ? new Failure("tls-failed", "no TLS session with the station that got as far as STARTDT con")
                    : new Failure("startdt-timeout", "no STARTDT con from the station");
        }
    }

    /** Closes without waiting long: closing a TLS socket whose handshake hangs is not bounded by anything else. */
    private static void close(Connection c) {
        if (c == null) {
            return;
        }
        Thread t = new Thread(c::close, "close");
        t.setDaemon(true);
        t.start();
        try {
            t.join(1000);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }

    @FunctionalInterface
    private interface UFrame {
        void run() throws IOException;
    }

    /** Runs a blocking STARTDT/STOPDT call with a deadline. Returns false when it was not confirmed in time. */
    private static boolean uFrame(ExecutorService exec, UFrame call, int timeoutMs) throws IOException {
        return uFrame(exec, call, timeoutMs, () -> false);
    }

    /** The same, giving up as soon as abort holds. */
    private static boolean uFrame(ExecutorService exec, UFrame call, int timeoutMs, BooleanSupplier abort)
            throws IOException {
        Future<?> f = exec.submit(() -> {
            call.run();
            return null;
        });
        long deadline = System.nanoTime() + timeoutMs * 1_000_000L;
        try {
            while (true) {
                long left = (deadline - System.nanoTime()) / 1_000_000L;
                try {
                    f.get(Math.max(0, Math.min(left, 50)), TimeUnit.MILLISECONDS);
                    return true;
                } catch (TimeoutException e) {
                    if (left <= 50 || abort.getAsBoolean()) {
                        f.cancel(true);
                        return false;
                    }
                }
            }
        } catch (ExecutionException e) {
            if (e.getCause() instanceof java.io.InterruptedIOException) {
                return false;
            }
            if (e.getCause() instanceof IOException) {
                throw (IOException) e.getCause();
            }
            throw new IllegalStateException(e.getCause());
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return false;
        }
    }
}
