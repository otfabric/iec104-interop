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

import org.openmuc.j60870.ASdu;
import org.openmuc.j60870.ASduType;
import org.openmuc.j60870.CauseOfTransmission;
import org.openmuc.j60870.ClientConnectionBuilder;
import org.openmuc.j60870.Connection;
import org.openmuc.j60870.ConnectionEventListener;
import org.openmuc.j60870.ie.IeDoubleCommand;
import org.openmuc.j60870.ie.IeDoubleCommand.DoubleCommandState;
import org.openmuc.j60870.ie.IeNormalizedValue;
import org.openmuc.j60870.ie.IeQualifierOfCounterInterrogation;
import org.openmuc.j60870.ie.IeQualifierOfInterrogation;
import org.openmuc.j60870.ie.IeQualifierOfSetPointCommand;
import org.openmuc.j60870.ie.IeRegulatingStepCommand;
import org.openmuc.j60870.ie.IeRegulatingStepCommand.StepCommandState;
import org.openmuc.j60870.ie.IeScaledValue;
import org.openmuc.j60870.ie.IeShortFloat;
import org.openmuc.j60870.ie.IeSingleCommand;
import org.openmuc.j60870.ie.IeTestSequenceCounter;
import org.openmuc.j60870.ie.IeTime56;
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
    private final JsonArray asdus = new JsonArray();
    private final JsonArray confirmations = new JsonArray();
    private int asduCount;
    private boolean closed;
    private boolean terminated;
    private int negativeCot;
    private boolean readAnswered;
    private ASduType expectType;
    private int expectIoa;
    private boolean expectRead;

    @Override
    public synchronized void newASdu(Connection connection, ASdu asdu) {
        asdus.add(Codec.asduJson(asdu));
        asduCount++;
        ASduType type = asdu.getTypeIdentification();
        int cot = asdu.getCauseOfTransmission().getId();
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
        case "monitor":
            durationMs = args.integer("--duration-ms", 1000);
            maxAsdus = args.integer("--max-asdus", 0);
            break;
        default:
            throw new Args.UsageException("client: unknown operation " + op);
        }
        Args.Apci apci = args.apci();
        args.finish("client " + op);

        ClientCommand state = new ClientCommand();
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
                con = new ClientConnectionBuilder(host)
                        .setPort(port)
                        .setConnectionTimeout(connectTimeoutMs)
                        .setMaxNumOfOutstandingIPdus(apci.k)
                        .setMaxUnconfirmedIPdusReceived(apci.w)
                        .setMaxTimeNoAckReceived(apci.t1 * 1000)
                        .setMaxTimeNoAckSent(apci.t2 * 1000)
                        .setMaxIdleTime(apci.t3 * 1000)
                        .setConnectionEventListener(state)
                        .build();
            } catch (IOException e) {
                exitCode = Main.EXIT_CONNECT_FAILED;
                throw new Failure("connect-failed", "cannot connect to the station: " + e.getMessage());
            }
            connected = true;
            con.setOriginatorAddress(oa);

            // startDataTransfer waits for the confirmation for up to t1; bound it by --timeout-ms instead.
            final Connection c = con;
            if (!uFrame(exec, c::startDataTransfer, timeoutMs)) {
                throw new Failure("startdt-timeout", "no STARTDT con from the station");
            }
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
            if (con != null) {
                con.close();
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
        if (op.equals("connect")) {
            r.addProperty("stopdtConfirmed", stopdt);
        }
        synchronized (state) {
            r.add("confirmations", state.confirmations.deepCopy());
            r.addProperty("terminated", state.terminated);
            r.add("asdus", state.asdus.deepCopy());
        }
        r.addProperty("elapsedMs", System.currentTimeMillis() - begin);
        Main.emit(r);
        return exitCode;
    }

    @FunctionalInterface
    private interface UFrame {
        void run() throws IOException;
    }

    /** Runs a blocking STARTDT/STOPDT call with a deadline. Returns false when it was not confirmed in time. */
    private static boolean uFrame(ExecutorService exec, UFrame call, int timeoutMs) throws IOException {
        Future<?> f = exec.submit(() -> {
            call.run();
            return null;
        });
        try {
            f.get(timeoutMs, TimeUnit.MILLISECONDS);
            return true;
        } catch (TimeoutException e) {
            f.cancel(true);
            return false;
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
