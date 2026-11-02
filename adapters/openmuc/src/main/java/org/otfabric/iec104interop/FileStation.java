// SPDX-License-Identifier: GPL-3.0-or-later
package org.otfabric.iec104interop;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;
import java.util.SortedMap;
import java.util.TreeMap;
import java.util.concurrent.atomic.AtomicReference;

import org.openmuc.j60870.ASdu;
import org.openmuc.j60870.ASduType;
import org.openmuc.j60870.CauseOfTransmission;
import org.openmuc.j60870.Connection;
import org.openmuc.j60870.ie.IeAckFileOrSectionQualifier;
import org.openmuc.j60870.ie.IeChecksum;
import org.openmuc.j60870.ie.IeFileReadyQualifier;
import org.openmuc.j60870.ie.IeFileSegment;
import org.openmuc.j60870.ie.IeLastSectionOrSegmentQualifier;
import org.openmuc.j60870.ie.IeLengthOfFileOrSection;
import org.openmuc.j60870.ie.IeNameOfFile;
import org.openmuc.j60870.ie.IeNameOfSection;
import org.openmuc.j60870.ie.IeSectionReadyQualifier;
import org.openmuc.j60870.ie.IeSelectAndCallQualifier;
import org.openmuc.j60870.ie.IeStatusOfFile;
import org.openmuc.j60870.ie.InformationElement;
import org.openmuc.j60870.ie.InformationObject;

import com.google.gson.JsonObject;

/**
 * The file transfer of the station, as docs/FIXTURES.md describes it. j60870 has the information elements of file
 * transfer but no procedure, so the adapter runs it.
 */
final class FileStation {
    /** So that the ASDU fits an APDU with three-octet addresses: see docs/FIXTURES.md. */
    static final int MAX_SEGMENT = 236;

    private static final CauseOfTransmission UNKNOWN_IOA = CauseOfTransmission.causeFor(47);

    /** The size of the sections of a stored file, which is the station's to choose. */
    private static final int STORED_SECTION = 2048;
    /** Entries of 16 octets in an ASDU of at most 249, 6 of them the data unit identifier. */
    private static final int ENTRIES_PER_ASDU = 15;

    private final Fixture fx;
    /** The stored uploads by address; guarded by itself. */
    private final SortedMap<Integer, Fixture.FileSpec> stored = new TreeMap<>();

    FileStation(Fixture fx) {
        this.fx = fx;
    }

    private Fixture.FileSpec find(int ioa) {
        for (Fixture.FileSpec f : fx.files) {
            if (f.ioa == ioa) {
                return f;
            }
        }
        synchronized (stored) {
            return stored.get(ioa);
        }
    }

    private void store(Fixture.Upload u, byte[] content) {
        Fixture.FileSpec f = new Fixture.FileSpec();
        f.ioa = u.ioa;
        f.name = u.name;
        f.size = content.length;
        f.sectionSize = Math.min(STORED_SECTION, content.length);
        f.stored = content;
        synchronized (stored) {
            stored.put(u.ioa, f);
        }
    }

    /** F_SC_NA_1 with cause 5. */
    void directory(Connection c, ASdu a) throws IOException {
        int scq = ((IeSelectAndCallQualifier) a.getInformationObjects()[0].getInformationElements()[0][2])
                .getRequest();
        if (scq != 0) {
            c.sendConfirmation(a, a.getCommonAddress(), true, CauseOfTransmission.UNKNOWN_CAUSE_OF_TRANSMISSION);
            return;
        }
        List<Fixture.FileSpec> all = new ArrayList<>(fx.files);
        synchronized (stored) {
            all.addAll(stored.values());
        }
        all.sort(Comparator.comparingInt(f -> f.ioa));
        if (all.isEmpty()) {
            refuse(c, a);
            return;
        }
        // Connection.sendDirectory cannot be used. It sends cause 13, and its one object with several sets of elements
        // and SQ=0 is what the constructor of ASdu refuses (ASdu.java:533, validateNonSequenceInformationObjects).
        Integer oa = a.getOriginatorAddress();
        for (int from = 0; from < all.size(); from += ENTRIES_PER_ASDU) {
            int to = Math.min(from + ENTRIES_PER_ASDU, all.size());
            InformationObject[] entries = new InformationObject[to - from];
            for (int i = from; i < to; i++) {
                Fixture.FileSpec f = all.get(i);
                IeStatusOfFile sof = i == all.size() - 1
                        ? new IeStatusOfFile(0, IeStatusOfFile.Flag.LAST_FILE_OF_DIRECTORY)
                        : new IeStatusOfFile(0);
                entries[i - from] = new InformationObject(f.ioa, new IeNameOfFile(f.name),
                        new IeLengthOfFileOrSection(f.size), sof, Codec.time(System.currentTimeMillis()));
            }
            c.send(new ASdu(ASduType.F_DR_TA_1, false, CauseOfTransmission.REQUEST, false, false,
                    oa == null ? 0 : oa, fx.commonAddress, entries));
        }
    }

    private ASdu asdu(ASdu request, ASduType type, int ioa, InformationElement... elements) {
        Integer oa = request.getOriginatorAddress();
        return new ASdu(type, false, CauseOfTransmission.FILE_TRANSFER, false, false, oa == null ? 0 : oa,
                fx.commonAddress, new InformationObject(ioa, elements));
    }

    private static void refuse(Connection c, ASdu request) throws IOException {
        c.sendConfirmation(request, request.getCommonAddress(), true, UNKNOWN_IOA);
    }

    /** A download in progress. */
    private static final class Download {
        final Fixture.FileSpec file;
        final byte[] content;
        int section;

        Download(Fixture.FileSpec file) {
            this.file = file;
            this.content = file.content();
        }

        int sections() {
            return (file.size + file.sectionSize - 1) / file.sectionSize;
        }

        int from(int nos) {
            return (nos - 1) * file.sectionSize;
        }

        int to(int nos) {
            return Math.min(nos * file.sectionSize, file.size);
        }
    }

    /** An upload in progress. */
    private static final class Upload {
        final Fixture.Upload spec;
        final int length;
        final ByteArrayOutputStream data = new ByteArrayOutputStream();
        ByteArrayOutputStream section;
        int sectionName;
        int sectionLength;

        Upload(Fixture.Upload spec, int length) {
            this.spec = spec;
            this.length = length;
        }

        boolean names(int ioa, int nof) {
            return spec.ioa == ioa && spec.name == nof;
        }
    }

    /**
     * The file transfers of one connection. j60870 calls the handlers one at a time, and closed() on its reader thread
     * while it holds the monitor of the connection, so no lock is held here while sending: a send waiting for the window
     * to open would never see that the connection closed.
     */
    final class Link {
        private final AtomicReference<Download> download = new AtomicReference<>();
        private final AtomicReference<Upload> upload = new AtomicReference<>();

        private void received(Upload u, boolean success) {
            if (u == null) {
                return;
            }
            byte[] data = u.data.toByteArray();
            JsonObject e = new JsonObject();
            e.addProperty("event", "file-received");
            e.addProperty("ioa", u.spec.ioa);
            e.addProperty("name", u.spec.name);
            e.addProperty("length", data.length);
            e.addProperty("sha256", Codec.sha256Hex(data));
            e.addProperty("success", success);
            Main.emit(e);
        }

        /** F_FR_NA_1 with cause 13. */
        void fileReady(Connection c, ASdu a) throws IOException {
            InformationObject io = a.getInformationObjects()[0];
            InformationElement[] e = io.getInformationElements()[0];
            int ioa = io.getInformationObjectAddress();
            IeNameOfFile nof = (IeNameOfFile) e[0];
            int length = ((IeLengthOfFileOrSection) e[1]).getValue();
            Fixture.Upload u = fx.upload(ioa);
            if (u == null || u.name != nof.getValue() || length < 1 || length > u.maxSize) {
                refuse(c, a);
                return;
            }
            received(upload.getAndSet(new Upload(u, length)), false);
            c.send(asdu(a, ASduType.F_SC_NA_1, ioa, nof, new IeNameOfSection(0), new IeSelectAndCallQualifier(2, 0)));
        }

        /** F_SR_NA_1 with cause 13. */
        void sectionReady(Connection c, ASdu a) throws IOException {
            InformationObject io = a.getInformationObjects()[0];
            InformationElement[] e = io.getInformationElements()[0];
            int ioa = io.getInformationObjectAddress();
            IeNameOfFile nof = (IeNameOfFile) e[0];
            IeNameOfSection nos = (IeNameOfSection) e[1];
            Upload u = upload.get();
            if (u == null || !u.names(ioa, nof.getValue()) || ((IeSectionReadyQualifier) e[3]).isSectionNotReady()) {
                refuse(c, a);
                return;
            }
            u.section = new ByteArrayOutputStream();
            u.sectionName = nos.getValue();
            u.sectionLength = ((IeLengthOfFileOrSection) e[2]).getValue();
            c.send(asdu(a, ASduType.F_SC_NA_1, ioa, nof, nos, new IeSelectAndCallQualifier(6, 0)));
        }

        /** F_SG_NA_1 with cause 13. */
        void segment(Connection c, ASdu a) throws IOException {
            InformationObject io = a.getInformationObjects()[0];
            InformationElement[] e = io.getInformationElements()[0];
            Upload u = upload.get();
            if (u == null || !u.names(io.getInformationObjectAddress(), ((IeNameOfFile) e[0]).getValue())
                    || u.section == null || u.sectionName != ((IeNameOfSection) e[1]).getValue()) {
                refuse(c, a);
                return;
            }
            byte[] octets = ((IeFileSegment) e[2]).getSegment();
            // More than was announced fails the comparison of the length; there is no need to keep it all.
            if (u.section.size() <= u.length) {
                u.section.write(octets, 0, octets.length);
            }
        }

        /** F_LS_NA_1 with cause 13. */
        void last(Connection c, ASdu a) throws IOException {
            InformationObject io = a.getInformationObjects()[0];
            InformationElement[] e = io.getInformationElements()[0];
            int ioa = io.getInformationObjectAddress();
            IeNameOfFile nof = (IeNameOfFile) e[0];
            IeNameOfSection nos = (IeNameOfSection) e[1];
            int lsq = ((IeLastSectionOrSegmentQualifier) e[2]).getValue();
            int chs = ((IeChecksum) e[3]).getValue();
            Upload u = upload.get();
            if (u == null || !u.names(ioa, nof.getValue())) {
                refuse(c, a);
                return;
            }
            int afq;
            if (lsq == 3) {
                byte[] section = u.section == null ? new byte[0] : u.section.toByteArray();
                boolean good = u.section != null && u.sectionName == nos.getValue()
                        && section.length == u.sectionLength
                        && Codec.checksum(section, 0, section.length) == chs
                        && u.data.size() + section.length <= u.length;
                if (good) {
                    u.data.write(section, 0, section.length);
                }
                u.section = null;
                afq = good ? 3 : 4;
            } else if (lsq == 1) {
                byte[] data = u.data.toByteArray();
                boolean good = data.length == u.length && Codec.checksum(data, 0, data.length) == chs;
                upload.set(null);
                // Stored and reported before the client hears of it: it may look straight away.
                if (good) {
                    store(u.spec, data);
                }
                received(u, good);
                afq = good ? 1 : 2;
            } else {
                refuse(c, a);
                return;
            }
            c.send(asdu(a, ASduType.F_AF_NA_1, ioa, nof, nos, new IeAckFileOrSectionQualifier(afq, 0)));
        }

        private void ended(Download d, boolean success) {
            if (d == null) {
                return;
            }
            JsonObject e = new JsonObject();
            e.addProperty("event", "file-transfer");
            e.addProperty("ioa", d.file.ioa);
            e.addProperty("name", d.file.name);
            e.addProperty("success", success);
            Main.emit(e);
        }

        /** F_SC_NA_1 with cause 13. */
        void call(Connection c, ASdu a) throws IOException {
            InformationObject io = a.getInformationObjects()[0];
            InformationElement[] e = io.getInformationElements()[0];
            int ioa = io.getInformationObjectAddress();
            IeNameOfFile nof = (IeNameOfFile) e[0];
            int nos = ((IeNameOfSection) e[1]).getValue();
            int scq = ((IeSelectAndCallQualifier) e[2]).getRequest();
            Download d = download.get();
            if (scq == 3 || scq == 7) {
                // Answered with nothing, whatever file it names.
                if (d != null && d.file.ioa == ioa && d.file.name == nof.getValue()) {
                    ended(download.getAndSet(null), false);
                }
                return;
            }
            if (scq == 1 || scq == 2) {
                Fixture.FileSpec f = find(ioa);
                if (f == null || f.name != nof.getValue()) {
                    refuse(c, a);
                    return;
                }
                if (scq == 1 || d == null || d.file != f) {
                    ended(download.getAndSet(new Download(f)), false);
                    d = download.get();
                }
                if (scq == 1) {
                    c.send(asdu(a, ASduType.F_FR_NA_1, ioa, nof, new IeLengthOfFileOrSection(f.size),
                            new IeFileReadyQualifier(0, false)));
                } else {
                    announceSection(c, a, d, 1);
                }
                return;
            }
            if (scq == 6 && d != null && d.file.ioa == ioa && d.file.name == nof.getValue() && nos >= 1
                    && nos <= d.sections()) {
                sendSection(c, a, d, nos);
                return;
            }
            refuse(c, a);
        }

        private void announceSection(Connection c, ASdu a, Download d, int nos) throws IOException {
            c.send(asdu(a, ASduType.F_SR_NA_1, d.file.ioa, new IeNameOfFile(d.file.name), new IeNameOfSection(nos),
                    new IeLengthOfFileOrSection(d.to(nos) - d.from(nos)), new IeSectionReadyQualifier(0, false)));
        }

        private void sendSection(Connection c, ASdu a, Download d, int nos) throws IOException {
            d.section = nos;
            IeNameOfFile nof = new IeNameOfFile(d.file.name);
            IeNameOfSection name = new IeNameOfSection(nos);
            int to = d.to(nos);
            for (int off = d.from(nos); off < to; off += MAX_SEGMENT) {
                c.send(asdu(a, ASduType.F_SG_NA_1, d.file.ioa, nof, name,
                        new IeFileSegment(d.content, off, Math.min(MAX_SEGMENT, to - off))));
            }
            c.send(asdu(a, ASduType.F_LS_NA_1, d.file.ioa, nof, name, new IeLastSectionOrSegmentQualifier(3),
                    new IeChecksum(Codec.checksum(d.content, d.from(nos), to))));
        }

        /** F_AF_NA_1 with cause 13. */
        void acknowledged(Connection c, ASdu a) throws IOException {
            InformationObject io = a.getInformationObjects()[0];
            InformationElement[] e = io.getInformationElements()[0];
            int afq = ((IeAckFileOrSectionQualifier) e[2]).getRequest();
            Download d = download.get();
            if (d == null || d.file.ioa != io.getInformationObjectAddress()
                    || d.file.name != ((IeNameOfFile) e[0]).getValue()) {
                refuse(c, a);
                return;
            }
            switch (afq) {
            case 3:
                if (d.section < d.sections()) {
                    announceSection(c, a, d, d.section + 1);
                } else {
                    c.send(asdu(a, ASduType.F_LS_NA_1, d.file.ioa, new IeNameOfFile(d.file.name),
                            new IeNameOfSection(d.sections()), new IeLastSectionOrSegmentQualifier(1),
                            new IeChecksum(Codec.checksum(d.content, 0, d.content.length))));
                }
                break;
            case 1:
                ended(download.getAndSet(null), true);
                break;
            default: // 2 and 4: not acknowledged
                ended(download.getAndSet(null), false);
                break;
            }
        }

        /** The connection ended. */
        void closed() {
            ended(download.getAndSet(null), false);
            received(upload.getAndSet(null), false);
        }
    }
}
