// SPDX-License-Identifier: GPL-3.0-or-later
package org.otfabric.iec104interop;

import java.io.IOException;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.SocketAddress;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.CountDownLatch;

import org.openmuc.j60870.ASdu;
import org.openmuc.j60870.ASduType;
import org.openmuc.j60870.CauseOfTransmission;
import org.openmuc.j60870.Connection;
import org.openmuc.j60870.ConnectionEventListener;
import org.openmuc.j60870.Server;
import org.openmuc.j60870.ServerEventListener;
import org.openmuc.j60870.ie.IeDoubleCommand;
import org.openmuc.j60870.ie.IeNormalizedValue;
import org.openmuc.j60870.ie.IeQualifierOfCounterInterrogation;
import org.openmuc.j60870.ie.IeQualifierOfInterrogation;
import org.openmuc.j60870.ie.IeQualifierOfSetPointCommand;
import org.openmuc.j60870.ie.IeRegulatingStepCommand;
import org.openmuc.j60870.ie.IeScaledValue;
import org.openmuc.j60870.ie.IeShortFloat;
import org.openmuc.j60870.ie.IeSingleCommand;
import org.openmuc.j60870.ie.IeTime56;
import org.openmuc.j60870.ie.InformationElement;
import org.openmuc.j60870.ie.InformationObject;

import com.google.gson.JsonObject;

/** The controlled station of the contract, driven by a fixture. */
final class ServerCommand {
    private static final int BROADCAST = 0xFFFF;
    /** Objects per ASDU when answering an interrogation; small enough for every type. */
    private static final int MAX_OBJECTS = 20;

    private static final CauseOfTransmission UNKNOWN_TYPE = CauseOfTransmission.causeFor(44);
    private static final CauseOfTransmission UNKNOWN_COT = CauseOfTransmission.causeFor(45);
    private static final CauseOfTransmission UNKNOWN_CA = CauseOfTransmission.causeFor(46);
    private static final CauseOfTransmission UNKNOWN_IOA = CauseOfTransmission.causeFor(47);

    private final Fixture fx;

    private ServerCommand(Fixture fx) {
        this.fx = fx;
    }

    private static JsonObject event(String name) {
        JsonObject e = new JsonObject();
        e.addProperty("event", name);
        return e;
    }

    private static String peer(Connection c) {
        SocketAddress a = c.getRemoteSocketAddress();
        if (a instanceof InetSocketAddress) {
            InetSocketAddress i = (InetSocketAddress) a;
            return i.getAddress().getHostAddress() + ":" + i.getPort();
        }
        return String.valueOf(a);
    }

    static int run(Args args) throws Exception {
        String fixturePath = args.string("--fixture", Main.DEFAULT_FIXTURE);
        String bind = args.string("--bind-address", "0.0.0.0");
        Path readyFile = Path.of(args.string("--ready-file", Main.DEFAULT_READY_FILE));
        int port = args.integer("--port", 2404);
        Args.Apci apci = args.apci();
        args.finish("server");
        if (port < 1 || port > 65535) {
            throw new Args.UsageException("--port: " + port + " is not a TCP port");
        }

        Fixture fx;
        try {
            fx = Fixture.load(fixturePath);
        } catch (Fixture.InvalidException e) {
            Main.log("fixture " + fixturePath + ": " + e.getMessage());
            return Main.EXIT_FIXTURE_INVALID;
        }
        Files.deleteIfExists(readyFile);

        ServerCommand station = new ServerCommand(fx);
        Server server = Server.builder()
                .setBindAddr(InetAddress.getByName(bind))
                .setPort(port)
                .setMaxConnections(16)
                .setMaxNumOfOutstandingIPdus(apci.k)
                .setMaxUnconfirmedIPdusReceived(apci.w)
                .setMaxTimeNoAckReceived(apci.t1 * 1000)
                .setMaxTimeNoAckSent(apci.t2 * 1000)
                .setMaxIdleTime(apci.t3 * 1000)
                .build();

        CountDownLatch stopped = new CountDownLatch(1);
        try {
            server.start(station.new Listener(stopped));
        } catch (IOException e) {
            Main.log("cannot listen on " + bind + ":" + port + ": " + e.getMessage());
            return Main.EXIT_OPERATION_FAILED;
        }

        // SIGTERM and SIGINT run the shutdown hook. The JVM would then exit with
        // 128+signal; the contract asks for 0 after a clean stop, hence halt(0).
        Runtime.getRuntime().addShutdownHook(new Thread(() -> {
            try {
                Files.deleteIfExists(readyFile);
            } catch (IOException e) {
                // nothing left to do
            }
            server.stop();
            Main.emit(event("stopped"));
            Runtime.getRuntime().halt(Main.EXIT_OK);
        }, "shutdown"));

        try {
            Files.writeString(readyFile, "ready\n");
        } catch (IOException e) {
            Main.log("warning: cannot write ready file " + readyFile);
        }
        JsonObject ready = event("ready");
        ready.addProperty("adapter", Main.ADAPTER);
        ready.addProperty("address", bind + ":" + port);
        ready.addProperty("fixture", fx.name);
        ready.addProperty("commonAddress", fx.commonAddress);
        Main.emit(ready);

        stopped.await();
        return Main.EXIT_OPERATION_FAILED; // the listener died without a signal
    }

    private final class Listener implements ServerEventListener {
        private final CountDownLatch stopped;

        Listener(CountDownLatch stopped) {
            this.stopped = stopped;
        }

        @Override
        public ConnectionEventListener connectionIndication(Connection connection) {
            JsonObject e = event("connection-opened");
            e.addProperty("peer", peer(connection));
            Main.emit(e);
            return new Session();
        }

        @Override
        public void serverStoppedListeningIndication(IOException e) {
            Main.log("server stopped listening: " + e);
            stopped.countDown();
        }

        @Override
        public void connectionAttemptFailed(IOException e) {
            Main.log("connection attempt failed: " + e);
        }
    }

    private final class Session implements ConnectionEventListener {
        @Override
        public void newASdu(Connection connection, ASdu asdu) {
            try {
                handle(connection, asdu);
            } catch (IOException e) {
                Main.log("send failed: " + e);
            } catch (RuntimeException e) {
                Main.log("cannot handle " + asdu.getTypeIdentification() + ": " + e);
            }
        }

        @Override
        public void connectionClosed(Connection connection, IOException cause) {
            if (cause != null && cause.getCause() != null) {
                // j60870 wraps what went wrong in its reader; a plain close has no cause.
                Main.log("connection closed: " + cause.getCause());
            }
            JsonObject e = event("connection-closed");
            e.addProperty("peer", peer(connection));
            Main.emit(e);
        }

        @Override
        public void dataTransferStateChanged(Connection connection, boolean stopped) {
            JsonObject e = event(stopped ? "data-transfer-stopped" : "data-transfer-started");
            e.addProperty("peer", peer(connection));
            Main.emit(e);
        }
    }

    private void reject(Connection c, ASdu asdu, CauseOfTransmission cot) throws IOException {
        c.sendConfirmation(asdu, asdu.getCommonAddress(), true, cot);
    }

    private void confirm(Connection c, ASdu asdu, boolean negative) throws IOException {
        c.sendConfirmation(asdu, fx.commonAddress, negative, CauseOfTransmission.ACTIVATION_CON);
    }

    private int originator(ASdu asdu) {
        Integer oa = asdu.getOriginatorAddress();
        return oa == null ? 0 : oa;
    }

    private void handle(Connection c, ASdu asdu) throws IOException {
        ASduType wire = asdu.getTypeIdentification();
        ASduType type = Codec.baseType(wire);
        int ca = asdu.getCommonAddress();
        if (ca != fx.commonAddress && ca != BROADCAST) {
            reject(c, asdu, UNKNOWN_CA);
            return;
        }
        CauseOfTransmission cot = asdu.getCauseOfTransmission();
        switch (type) {
        case C_IC_NA_1:
            if (cot != CauseOfTransmission.ACTIVATION) {
                reject(c, asdu, UNKNOWN_COT);
                return;
            }
            interrogation(c, asdu);
            return;
        case C_CI_NA_1:
            if (cot != CauseOfTransmission.ACTIVATION) {
                reject(c, asdu, UNKNOWN_COT);
                return;
            }
            counterInterrogation(c, asdu);
            return;
        case C_RD_NA_1:
            if (cot != CauseOfTransmission.REQUEST) {
                reject(c, asdu, UNKNOWN_COT);
                return;
            }
            read(c, asdu);
            return;
        case C_CS_NA_1: {
            if (cot != CauseOfTransmission.ACTIVATION) {
                reject(c, asdu, UNKNOWN_COT);
                return;
            }
            IeTime56 t = (IeTime56) asdu.getInformationObjects()[0].getInformationElements()[0][0];
            JsonObject e = event("clock-sync");
            e.addProperty("time", Codec.formatTime(t.getTimestamp(1970, Codec.UTC)));
            Main.emit(e);
            confirm(c, asdu, false);
            return;
        }
        case C_TS_TA_1:
            if (cot != CauseOfTransmission.ACTIVATION) {
                reject(c, asdu, UNKNOWN_COT);
                return;
            }
            confirm(c, asdu, false);
            return;
        case C_SC_NA_1:
        case C_DC_NA_1:
        case C_RC_NA_1:
        case C_SE_NA_1:
        case C_SE_NB_1:
        case C_SE_NC_1:
            if (cot != CauseOfTransmission.ACTIVATION && cot != CauseOfTransmission.DEACTIVATION) {
                reject(c, asdu, UNKNOWN_COT);
                return;
            }
            command(c, asdu, wire, type);
            return;
        default:
            reject(c, asdu, UNKNOWN_TYPE);
        }
    }

    /**
     * Sends the points selected by counters (true: integrated totals, false: everything else) in ascending address
     * order, packing consecutive points of one type into one ASDU.
     */
    private void sendPoints(Connection c, CauseOfTransmission cot, int oa, boolean counters) throws IOException {
        List<InformationObject> batch = new ArrayList<>();
        ASduType current = null;
        List<ASdu> out = new ArrayList<>();
        synchronized (fx) {
            for (Fixture.Point p : fx.points) {
                if ((p.type == ASduType.M_IT_NA_1) != counters) {
                    continue;
                }
                if (!batch.isEmpty() && (p.type != current || batch.size() >= MAX_OBJECTS)) {
                    out.add(new ASdu(current, false, cot, false, false, oa, fx.commonAddress,
                            batch.toArray(new InformationObject[0])));
                    batch.clear();
                }
                current = p.type;
                batch.add(Codec.pointObject(p, null));
            }
            if (!batch.isEmpty()) {
                out.add(new ASdu(current, false, cot, false, false, oa, fx.commonAddress,
                        batch.toArray(new InformationObject[0])));
            }
        }
        for (ASdu a : out) {
            c.send(a);
        }
    }

    private void interrogation(Connection c, ASdu asdu) throws IOException {
        int qoi = ((IeQualifierOfInterrogation) asdu.getInformationObjects()[0].getInformationElements()[0][0])
                .getValue();
        boolean accepted = qoi == 20;
        JsonObject e = event("interrogation");
        e.addProperty("commonAddress", asdu.getCommonAddress());
        e.addProperty("qoi", qoi);
        e.addProperty("accepted", accepted);
        Main.emit(e);
        if (!accepted) {
            confirm(c, asdu, true);
            return;
        }
        confirm(c, asdu, false);
        sendPoints(c, CauseOfTransmission.INTERROGATED_BY_STATION, originator(asdu), false);
        c.sendActivationTermination(asdu, fx.commonAddress);
    }

    private void counterInterrogation(Connection c, ASdu asdu) throws IOException {
        IeQualifierOfCounterInterrogation q = (IeQualifierOfCounterInterrogation) asdu.getInformationObjects()[0]
                .getInformationElements()[0][0];
        // Only "general request counter" with "read" (no freeze, no reset).
        boolean accepted = q.getRequest() == 5 && q.getFreeze() == 0;
        JsonObject e = event("counter-interrogation");
        e.addProperty("commonAddress", asdu.getCommonAddress());
        e.addProperty("qcc", q.getRequest() | q.getFreeze() << 6);
        e.addProperty("accepted", accepted);
        Main.emit(e);
        if (!accepted) {
            confirm(c, asdu, true);
            return;
        }
        confirm(c, asdu, false);
        sendPoints(c, CauseOfTransmission.REQUESTED_BY_GENERAL_COUNTER, originator(asdu), true);
        c.sendActivationTermination(asdu, fx.commonAddress);
    }

    private void read(Connection c, ASdu asdu) throws IOException {
        int ioa = asdu.getInformationObjects()[0].getInformationObjectAddress();
        InformationObject io = null;
        ASduType type = null;
        synchronized (fx) {
            Fixture.Point p = fx.point(ioa);
            if (p != null) {
                io = Codec.pointObject(p, null);
                type = p.type;
            }
        }
        JsonObject e = event("read");
        e.addProperty("ioa", ioa);
        e.addProperty("accepted", io != null);
        Main.emit(e);
        if (io == null) {
            reject(c, asdu, UNKNOWN_IOA);
            return;
        }
        c.send(new ASdu(type, false, CauseOfTransmission.REQUEST, false, false, originator(asdu), fx.commonAddress,
                io));
    }

    private void command(Connection c, ASdu asdu, ASduType wire, ASduType type) throws IOException {
        InformationObject io = asdu.getInformationObjects()[0];
        InformationElement[] el = io.getInformationElements()[0];
        int ioa = io.getInformationObjectAddress();
        CauseOfTransmission cot = asdu.getCauseOfTransmission();

        JsonObject e = event("command");
        e.addProperty("type", wire.name());
        e.addProperty("ioa", ioa);
        e.addProperty("cot", cot.getId());

        String outcome;
        InformationObject report = null;
        ASduType reportType = null;
        int reportCause = 0;
        synchronized (fx) {
            Fixture.Command cmd = fx.command(ioa);
            if (cmd == null || cmd.type != type) {
                e.addProperty("outcome", "unknown-ioa");
                Main.emit(e);
                reject(c, asdu, UNKNOWN_IOA);
                return;
            }
            double value;
            boolean select;
            boolean valid = true;
            switch (type) {
            case C_SC_NA_1: {
                IeSingleCommand v = (IeSingleCommand) el[0];
                value = v.isCommandStateOn() ? 1 : 0;
                select = v.isSelect();
                e.addProperty("value", v.isCommandStateOn());
                break;
            }
            case C_DC_NA_1: {
                IeDoubleCommand v = (IeDoubleCommand) el[0];
                value = v.getCommandState().getId();
                select = v.isSelect();
                valid = value == 1 || value == 2;
                e.addProperty("value", (int) value);
                break;
            }
            case C_RC_NA_1: {
                IeRegulatingStepCommand v = (IeRegulatingStepCommand) el[0];
                value = v.getCommandState().getId();
                select = v.isSelect();
                valid = value == 1 || value == 2;
                e.addProperty("value", (int) value);
                break;
            }
            case C_SE_NA_1:
                value = ((IeNormalizedValue) el[0]).getUnnormalizedValue();
                select = ((IeQualifierOfSetPointCommand) el[1]).isSelect();
                e.addProperty("value", (int) value);
                break;
            case C_SE_NB_1:
                value = ((IeScaledValue) el[0]).getUnnormalizedValue();
                select = ((IeQualifierOfSetPointCommand) el[1]).isSelect();
                e.addProperty("value", (int) value);
                break;
            default: // C_SE_NC_1
                value = ((IeShortFloat) el[0]).getValue();
                select = ((IeQualifierOfSetPointCommand) el[1]).isSelect();
                e.addProperty("value", value);
                break;
            }
            e.addProperty("select", select);

            if (cot == CauseOfTransmission.DEACTIVATION) {
                cmd.selected = false;
                outcome = "deactivated";
            } else if (!valid) {
                outcome = "rejected";
            } else if (select) {
                cmd.selected = true;
                outcome = "selected";
            } else if (cmd.selectRequired && !cmd.selected) {
                outcome = "rejected";
            } else {
                cmd.selected = false;
                Fixture.Point t = cmd.target;
                double next = value;
                if (type == ASduType.C_RC_NA_1) {
                    next = t.value + (value == 2 ? 1 : -1);
                }
                if (type == ASduType.C_RC_NA_1 && (next < -64 || next > 63)) {
                    outcome = "rejected"; // the step position is at its limit
                } else {
                    t.value = next;
                    report = Codec.pointObject(t, System.currentTimeMillis());
                    reportType = Codec.timeTagged(t.type);
                    reportCause = cmd.reportCause;
                    outcome = "executed";
                }
            }
        }
        e.addProperty("outcome", outcome);
        Main.emit(e);

        switch (outcome) {
        case "deactivated":
            c.sendConfirmation(asdu, fx.commonAddress, false, CauseOfTransmission.DEACTIVATION_CON);
            break;
        case "rejected":
            confirm(c, asdu, true);
            break;
        default:
            confirm(c, asdu, false);
            if (report != null) {
                // Confirmation, then the new state of the target, then termination.
                c.send(new ASdu(reportType, false, CauseOfTransmission.causeFor(reportCause), false, false, 0,
                        fx.commonAddress, report));
                c.sendActivationTermination(asdu, fx.commonAddress);
            }
        }
    }
}
