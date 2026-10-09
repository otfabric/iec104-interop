// SPDX-License-Identifier: GPL-3.0-or-later
package org.otfabric.iec104interop;

import java.io.IOException;
import java.io.PrintStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.TimeZone;
import java.util.jar.Manifest;

import com.google.gson.Gson;
import com.google.gson.GsonBuilder;
import com.google.gson.JsonArray;
import com.google.gson.JsonElement;
import com.google.gson.JsonObject;

/**
 * iec104-interop adapter for OpenMUC j60870.
 *
 * <p>
 * This program links j60870 (GPL-3.0) and is distributed under the same licence. It implements the container contract
 * in docs/CONTAINER_CONTRACT.md.
 */
public final class Main {
    static final String ADAPTER = "openmuc";
    static final String SCHEMA_VERSION = "1.0";
    static final String DEFAULT_FIXTURE = "/fixtures/baseline/fixture.json";
    static final String DEFAULT_READY_FILE = "/run/iec104-interop/ready";

    static final int EXIT_OK = 0;
    static final int EXIT_OPERATION_FAILED = 1;
    static final int EXIT_USAGE = 2;
    static final int EXIT_CONNECT_FAILED = 3;
    static final int EXIT_FIXTURE_INVALID = 4;

    private static final Gson GSON = new GsonBuilder().serializeNulls().disableHtmlEscaping().create();
    /** The contract's stdout; System.out is redirected so that nothing else can write there. */
    private static PrintStream out;

    private static final String USAGE = String.join("\n",
            "usage: iec104-interop <command>",
            "",
            "  server [--fixture PATH] [--bind-address ADDR] [--port N] [--ready-file PATH] [APCI]",
            "  client <operation> --host HOST [--port N] [--common-address N] [--originator-address N]",
            "         [--timeout-ms N] [--connect-timeout-ms N] [--collect-ms N] [APCI] [operation flags]",
            "      connect             [--hold-ms N]",
            "      interrogate         [--qoi N]",
            "      counter-interrogate [--qcc N]",
            "      read                --ioa N",
            "      clock-sync          [--time 2026-01-02T03:04:05.678Z]",
            "      test-command",
            "      command             --type T --ioa N --value V [--mode direct|select|sbo|cancel]",
            "                          [--qualifier N] [--with-time]",
            "      monitor             [--duration-ms N] [--max-asdus N]",
            "  print-capabilities",
            "  print-fixture [NAME]",
            "  version",
            "",
            "  APCI: [--k N] [--w N] [--t0 S] [--t1 S] [--t2 S] [--t3 S]",
            "",
            "See docs/CONTAINER_CONTRACT.md in https://github.com/otfabric/iec104-interop",
            "");

    private Main() {
    }

    /** Prints one JSON document on a single line of stdout. */
    static synchronized void emit(JsonElement doc) {
        out.println(GSON.toJson(doc));
        out.flush();
    }

    static synchronized void log(String message) {
        System.err.println("[" + ADAPTER + "] " + message);
    }

    static String adapterVersion() {
        String v = System.getenv("ADAPTER_VERSION");
        return v == null || v.isEmpty() ? "dev" : v;
    }

    static String upstreamVersion() {
        try (var in = Main.class.getResourceAsStream("/META-INF/MANIFEST.MF")) {
            if (in != null) {
                String v = new Manifest(in).getMainAttributes().getValue("J60870-Version");
                if (v != null) {
                    return v;
                }
            }
        } catch (IOException e) {
            // fall through
        }
        return "unknown";
    }

    private static JsonArray strings(String... values) {
        JsonArray a = new JsonArray();
        for (String v : values) {
            a.add(v);
        }
        return a;
    }

    private static int printCapabilities() {
        JsonObject c = new JsonObject();
        c.addProperty("schemaVersion", SCHEMA_VERSION);
        c.addProperty("adapter", ADAPTER);
        c.addProperty("adapterVersion", adapterVersion());
        c.addProperty("protocol", "iec60870-5-104");
        JsonObject up = new JsonObject();
        up.addProperty("name", "j60870");
        up.addProperty("version", upstreamVersion());
        up.addProperty("revision", "org.openmuc:j60870:" + upstreamVersion());
        up.addProperty("license", "GPL-3.0");
        up.addProperty("language", "Java");
        // Not a wrapper around another adapter's protocol engine.
        up.addProperty("independentEngine", true);
        c.add("upstream", up);

        JsonObject roles = new JsonObject();
        roles.addProperty("server", true);
        roles.addProperty("client", true);
        c.add("roles", roles);

        JsonObject f = new JsonObject();
        f.addProperty("generalInterrogation", true);
        f.addProperty("groupInterrogation", false);
        f.addProperty("counterInterrogation", true);
        f.addProperty("read", true);
        f.addProperty("clockSync", true);
        f.addProperty("testCommand", true);
        f.addProperty("directCommands", true);
        f.addProperty("selectBeforeOperate", true);
        f.addProperty("commandDeactivation", true);
        f.addProperty("timeTaggedCommands", true);
        f.addProperty("spontaneousOnCommand", true);
        f.addProperty("apciParameters", true);
        f.addProperty("multipleConnections", true);
        f.addProperty("pointQualityOnBitstring", false);
        f.addProperty("tls", false);
        f.addProperty("fileTransfer", false);
        c.add("features", f);

        c.add("pointTypes", strings("M_SP_NA_1", "M_DP_NA_1", "M_ST_NA_1", "M_BO_NA_1", "M_ME_NA_1", "M_ME_NB_1",
                "M_ME_NC_1", "M_IT_NA_1"));
        c.add("commandTypes",
                strings("C_SC_NA_1", "C_DC_NA_1", "C_RC_NA_1", "C_SE_NA_1", "C_SE_NB_1", "C_SE_NC_1"));
        c.add("clientOperations", strings("connect", "interrogate", "counter-interrogate", "read", "clock-sync",
                "test-command", "command", "monitor"));
        emit(c);
        return EXIT_OK;
    }

    private static int printFixture(String name) {
        if (name.contains("/") || name.contains("..")) {
            log("print-fixture: '" + name + "' is not a fixture name");
            return EXIT_USAGE;
        }
        try {
            out.write(Files.readAllBytes(Path.of("/fixtures", name, "fixture.json")));
            out.flush();
            return EXIT_OK;
        } catch (IOException e) {
            log("print-fixture: no fixture named '" + name + "' in this image");
            return EXIT_OPERATION_FAILED;
        }
    }

    private static int run(String[] argv) throws Exception {
        if (argv.length < 1) {
            System.err.print(USAGE);
            return EXIT_USAGE;
        }
        switch (argv[0]) {
        case "server":
            return ServerCommand.run(new Args(argv, 1));
        case "client":
            if (argv.length < 2) {
                System.err.print(USAGE);
                return EXIT_USAGE;
            }
            return ClientCommand.run(argv[1], new Args(argv, 2));
        case "print-capabilities":
            return printCapabilities();
        case "print-fixture":
            return printFixture(argv.length > 1 ? argv[1] : "baseline");
        case "version":
            out.println(ADAPTER + " " + adapterVersion() + " (j60870 " + upstreamVersion() + ")");
            return EXIT_OK;
        case "help":
        case "--help":
        case "-h":
            out.print(USAGE);
            return EXIT_OK;
        default:
            System.err.print(USAGE);
            return EXIT_USAGE;
        }
    }

    public static void main(String[] argv) {
        // Time tags are UTC on the wire; j60870 converts through the default zone.
        TimeZone.setDefault(Codec.UTC);
        out = System.out;
        System.setOut(System.err);
        int code;
        try {
            code = run(argv);
        } catch (Args.UsageException e) {
            log(e.getMessage());
            code = EXIT_USAGE;
        } catch (Exception e) {
            log("fatal: " + e);
            code = EXIT_OPERATION_FAILED;
        }
        out.flush();
        // j60870 keeps non-daemon threads; exit explicitly with the contract's code.
        System.exit(code);
    }
}
