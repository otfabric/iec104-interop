// SPDX-License-Identifier: GPL-3.0-or-later
package org.otfabric.iec104interop;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;
import java.util.Map;
import java.util.Set;

import org.openmuc.j60870.ASduType;

import com.google.gson.JsonArray;
import com.google.gson.JsonElement;
import com.google.gson.JsonObject;
import com.google.gson.JsonParseException;
import com.google.gson.JsonParser;
import com.google.gson.JsonPrimitive;

/** The simulated station: see docs/FIXTURES.md. The rules mirror scripts/validate-fixtures.py. */
final class Fixture {
    static final class InvalidException extends Exception {
        private static final long serialVersionUID = 1L;

        InvalidException(String message) {
            super(message);
        }
    }

    static final class Point {
        int ioa;
        ASduType type;
        /** Raw NVA for M_ME_NA_1; 0 or 1 for M_SP_NA_1. */
        double value;
        boolean transientState;
        int sequence;
        Set<String> quality = Set.of();
    }

    static final class Command {
        int ioa;
        ASduType type;
        Point target;
        int reportCause = 11;
        boolean selectRequired;
        boolean selected;
    }

    static final Map<ASduType, ASduType> COMMAND_TARGET = Map.of(
            ASduType.C_SC_NA_1, ASduType.M_SP_NA_1,
            ASduType.C_DC_NA_1, ASduType.M_DP_NA_1,
            ASduType.C_RC_NA_1, ASduType.M_ST_NA_1,
            ASduType.C_SE_NA_1, ASduType.M_ME_NA_1,
            ASduType.C_SE_NB_1, ASduType.M_ME_NB_1,
            ASduType.C_SE_NC_1, ASduType.M_ME_NC_1);

    private static final Set<String> QDS = Set.of("OV", "BL", "SB", "NT", "IV");
    private static final Set<String> SIQ = Set.of("BL", "SB", "NT", "IV");
    private static final Set<String> BCR = Set.of("CY", "CA", "IV");

    String name;
    int commonAddress;
    /** Sorted by address. */
    final List<Point> points = new ArrayList<>();
    final List<Command> commands = new ArrayList<>();

    Point point(int ioa) {
        for (Point p : points) {
            if (p.ioa == ioa) {
                return p;
            }
        }
        return null;
    }

    Command command(int ioa) {
        for (Command c : commands) {
            if (c.ioa == ioa) {
                return c;
            }
        }
        return null;
    }

    private static boolean isInteger(JsonElement e) {
        if (e == null || !e.isJsonPrimitive() || !e.getAsJsonPrimitive().isNumber()) {
            return false;
        }
        double d = e.getAsDouble();
        return d == Math.rint(d);
    }

    private static boolean isBoolean(JsonElement e) {
        return e != null && e.isJsonPrimitive() && e.getAsJsonPrimitive().isBoolean();
    }

    private static boolean isNumber(JsonElement e) {
        return e != null && e.isJsonPrimitive() && e.getAsJsonPrimitive().isNumber();
    }

    private static boolean inRange(JsonElement e, double lo, double hi) {
        return isInteger(e) && e.getAsDouble() >= lo && e.getAsDouble() <= hi;
    }

    private static ASduType type(JsonElement e) {
        if (e == null || !e.isJsonPrimitive()) {
            return null;
        }
        try {
            return ASduType.valueOf(e.getAsString());
        } catch (IllegalArgumentException ex) {
            return null;
        }
    }

    static Fixture load(String path) throws InvalidException {
        JsonObject root;
        try {
            String text = Files.readString(Path.of(path), StandardCharsets.UTF_8);
            root = JsonParser.parseString(text).getAsJsonObject();
        } catch (IOException e) {
            throw new InvalidException("cannot read fixture " + path);
        } catch (JsonParseException | IllegalStateException e) {
            throw new InvalidException("fixture is not a JSON object");
        }

        JsonElement version = root.get("schemaVersion");
        if (version == null || !version.equals(new JsonPrimitive(Main.SCHEMA_VERSION))) {
            throw new InvalidException("unsupported schemaVersion, want " + Main.SCHEMA_VERSION);
        }
        Fixture fx = new Fixture();
        JsonElement name = root.get("name");
        JsonElement station = root.get("station");
        JsonElement ca = station != null && station.isJsonObject() ? station.getAsJsonObject().get("commonAddress")
                : null;
        JsonElement points = root.get("points");
        if (name == null || !name.isJsonPrimitive() || !inRange(ca, 1, 65534) || points == null
                || !points.isJsonArray() || points.getAsJsonArray().isEmpty()) {
            throw new InvalidException("fixture needs name, station.commonAddress (1..65534) and points");
        }
        fx.name = name.getAsString();
        fx.commonAddress = ca.getAsInt();

        for (JsonElement item : points.getAsJsonArray()) {
            if (!item.isJsonObject()) {
                throw new InvalidException("points must be objects");
            }
            JsonObject o = item.getAsJsonObject();
            Point p = new Point();
            if (!inRange(o.get("ioa"), 1, 0xFFFFFF)) {
                throw new InvalidException("point " + fx.points.size() + ": needs ioa (1..16777215) and type");
            }
            p.ioa = o.get("ioa").getAsInt();
            p.type = type(o.get("type"));
            if (fx.point(p.ioa) != null) {
                throw new InvalidException("point " + p.ioa + ": duplicate information object address");
            }
            JsonElement v = o.get("value");
            boolean valid;
            Set<String> allowed = QDS;
            if (p.type == null) {
                throw new InvalidException("point " + p.ioa + ": unsupported type " + o.get("type"));
            }
            switch (p.type) {
            case M_SP_NA_1:
                valid = isBoolean(v);
                allowed = SIQ;
                break;
            case M_DP_NA_1:
                valid = inRange(v, 0, 3);
                allowed = SIQ;
                break;
            case M_ST_NA_1:
                valid = inRange(v, -64, 63);
                break;
            case M_BO_NA_1:
                valid = inRange(v, 0, 4294967295.0);
                // Kept empty for every adapter: lib60870 cannot set it.
                allowed = Set.of();
                break;
            case M_ME_NA_1:
            case M_ME_NB_1:
                valid = inRange(v, -32768, 32767);
                break;
            case M_ME_NC_1:
                valid = isNumber(v);
                break;
            case M_IT_NA_1:
                valid = inRange(v, -2147483648.0, 2147483647.0);
                allowed = BCR;
                break;
            default:
                throw new InvalidException("point " + p.ioa + ": unsupported type " + p.type);
            }
            if (!valid) {
                throw new InvalidException("point " + p.ioa + ": value is not valid for " + p.type);
            }
            p.value = isBoolean(v) ? (v.getAsBoolean() ? 1 : 0) : v.getAsDouble();

            if (o.has("transient")) {
                if (p.type != ASduType.M_ST_NA_1 || !isBoolean(o.get("transient"))) {
                    throw new InvalidException("point " + p.ioa + ": transient is a boolean of M_ST_NA_1 only");
                }
                p.transientState = o.get("transient").getAsBoolean();
            }
            if (o.has("sequence")) {
                if (p.type != ASduType.M_IT_NA_1 || !inRange(o.get("sequence"), 0, 31)) {
                    throw new InvalidException("point " + p.ioa + ": sequence is 0..31 of M_IT_NA_1 only");
                }
                p.sequence = o.get("sequence").getAsInt();
            }
            if (o.has("quality")) {
                JsonElement q = o.get("quality");
                Set<String> flags = new java.util.TreeSet<>();
                if (!q.isJsonArray()) {
                    throw new InvalidException("point " + p.ioa + ": quality is not valid for " + p.type);
                }
                for (JsonElement f : (JsonArray) q) {
                    if (!f.isJsonPrimitive() || !allowed.contains(f.getAsString())) {
                        throw new InvalidException("point " + p.ioa + ": quality is not valid for " + p.type);
                    }
                    flags.add(f.getAsString());
                }
                p.quality = flags;
            }
            fx.points.add(p);
        }
        fx.points.sort(Comparator.comparingInt(p -> p.ioa));

        JsonElement commands = root.get("commands");
        if (commands != null) {
            if (!commands.isJsonArray()) {
                throw new InvalidException("commands must be an array");
            }
            for (JsonElement item : commands.getAsJsonArray()) {
                if (!item.isJsonObject()) {
                    throw new InvalidException("commands must be objects");
                }
                JsonObject o = item.getAsJsonObject();
                Command c = new Command();
                if (!inRange(o.get("ioa"), 1, 0xFFFFFF) || !isInteger(o.get("target"))) {
                    throw new InvalidException("command " + fx.commands.size() + ": needs ioa, type and target");
                }
                c.ioa = o.get("ioa").getAsInt();
                c.type = type(o.get("type"));
                ASduType want = c.type == null ? null : COMMAND_TARGET.get(c.type);
                if (want == null) {
                    throw new InvalidException("command " + c.ioa + ": unsupported type " + o.get("type"));
                }
                if (fx.command(c.ioa) != null || fx.point(c.ioa) != null) {
                    throw new InvalidException("command " + c.ioa + ": duplicate information object address");
                }
                c.target = fx.point(o.get("target").getAsInt());
                if (c.target == null || c.target.type != want) {
                    throw new InvalidException("command " + c.ioa + ": target must be a point of type " + want);
                }
                if (o.has("reportCause")) {
                    JsonElement rc = o.get("reportCause");
                    if (!isInteger(rc) || (rc.getAsInt() != 3 && rc.getAsInt() != 11)) {
                        throw new InvalidException("command " + c.ioa + ": reportCause must be 3 or 11");
                    }
                    c.reportCause = rc.getAsInt();
                }
                if (o.has("selectRequired")) {
                    if (!isBoolean(o.get("selectRequired"))) {
                        throw new InvalidException("command " + c.ioa + ": selectRequired must be a boolean");
                    }
                    c.selectRequired = o.get("selectRequired").getAsBoolean();
                }
                fx.commands.add(c);
            }
        }
        return fx;
    }
}
