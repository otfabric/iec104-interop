// SPDX-License-Identifier: GPL-3.0-or-later
package org.otfabric.iec104interop;

import java.util.ArrayList;
import java.util.List;

/** Command-line options of the form {@code --name value} and bare {@code --flag}. */
final class Args {
    /** Thrown for anything the caller got wrong; maps to exit code 2. */
    static final class UsageException extends RuntimeException {
        private static final long serialVersionUID = 1L;

        UsageException(String message) {
            super(message);
        }
    }

    private final List<String> rest;

    Args(String[] argv, int from) {
        rest = new ArrayList<>();
        for (int i = from; i < argv.length; i++) {
            rest.add(argv[i]);
        }
    }

    String string(String name, String def) {
        int i = rest.indexOf(name);
        if (i < 0 || i + 1 >= rest.size()) {
            return def;
        }
        String v = rest.get(i + 1);
        rest.remove(i + 1);
        rest.remove(i);
        return v;
    }

    int integer(String name, int def) {
        String s = string(name, null);
        if (s == null) {
            return def;
        }
        try {
            return Integer.parseInt(s);
        } catch (NumberFormatException e) {
            throw new UsageException(name + ": '" + s + "' is not an integer");
        }
    }

    boolean flag(String name) {
        return rest.remove(name);
    }

    /** Fails when an argument was not consumed. */
    void finish(String context) {
        if (!rest.isEmpty()) {
            throw new UsageException(context + ": unknown argument " + rest.get(0));
        }
    }

    /** The APCI parameters, in the seconds of the contract. */
    static final class Apci {
        int k;
        int w;
        int t0;
        int t1;
        int t2;
        int t3;
    }

    Apci apci() {
        Apci p = new Apci();
        p.k = integer("--k", 12);
        p.w = integer("--w", 8);
        p.t0 = integer("--t0", 30);
        p.t1 = integer("--t1", 15);
        p.t2 = integer("--t2", 10);
        p.t3 = integer("--t3", 20);
        if (p.k < 1 || p.k > 32767 || p.w < 1 || p.w > p.k || p.t0 < 1 || p.t1 < 1 || p.t2 < 1 || p.t2 >= p.t1) {
            throw new UsageException(
                    "invalid APCI parameters: need 1 <= w <= k <= 32767, t0 > 0, 0 < t2 < t1, t3 >= 0 (seconds)");
        }
        // j60870 cannot switch the idle test off and requires t3 >= t1.
        if (p.t3 < p.t1) {
            throw new UsageException("j60870 requires t3 >= t1; t3 = 0 (idle test disabled) is not supported");
        }
        return p;
    }
}
