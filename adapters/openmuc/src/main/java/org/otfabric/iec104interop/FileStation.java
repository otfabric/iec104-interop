// SPDX-License-Identifier: GPL-3.0-or-later
package org.otfabric.iec104interop;

import java.io.IOException;
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

    private final Fixture fx;

    FileStation(Fixture fx) {
        this.fx = fx;
    }

    private Fixture.FileSpec find(int ioa) {
        for (Fixture.FileSpec f : fx.files) {
            if (f.ioa == ioa) {
                return f;
            }
        }
        return null;
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

    /**
     * The file transfers of one connection. j60870 calls the handlers one at a time, and closed() on its reader thread
     * while it holds the monitor of the connection, so no lock is held here while sending: a send waiting for the window
     * to open would never see that the connection closed.
     */
    final class Link {
        private final AtomicReference<Download> download = new AtomicReference<>();

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
                    sectionReady(c, a, d, 1);
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

        private void sectionReady(Connection c, ASdu a, Download d, int nos) throws IOException {
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
                    sectionReady(c, a, d, d.section + 1);
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
        }
    }
}
