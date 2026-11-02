// SPDX-License-Identifier: GPL-3.0-or-later
package org.otfabric.iec104interop;

import java.io.FilterInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetAddress;
import java.net.Socket;
import java.net.SocketAddress;

import javax.net.SocketFactory;

/**
 * Counts the I frames of a connection, and where in the stream STARTDT con and STOPDT con were, for the redundancy
 * operation.
 *
 * <p>
 * The listener of j60870 cannot say which I frames are outside the time a connection is started: after
 * Connection.stopDataTransfer it has no listener and drops what arrives (Connection.java:563, 406), and it tells nobody
 * when STARTDT con arrives: startDataTransfer returns on another thread, in a race with an I frame right behind the
 * confirmation. The order of the frames in the stream is exact, and the socket is the one thing the public API lets the
 * adapter replace.
 */
final class ApduTap extends SocketFactory {
    private static final int START = 0x68;
    private static final int STARTDT_CON = 0x0B;
    private static final int STOPDT_CON = 0x23;

    private final SocketFactory inner;

    // Guarded by this.
    private int frames;
    private int atStart = -1;
    private int atStop = -1;
    /** 0: between frames; 1: the length is next; 2: the first octet of the control field is next; 3: the rest. */
    private int state;
    private int left;

    ApduTap(SocketFactory inner) {
        this.inner = inner;
    }

    private synchronized void feed(byte[] b, int off, int len) {
        for (int i = off; i < off + len; i++) {
            int octet = b[i] & 0xFF;
            switch (state) {
            case 0:
                state = octet == START ? 1 : 0;
                break;
            case 1:
                left = octet;
                state = left == 0 ? 0 : 2;
                break;
            case 2:
                if ((octet & 0x01) == 0) {
                    frames++;
                } else if (octet == STARTDT_CON && atStart < 0) {
                    atStart = frames;
                } else if (octet == STOPDT_CON && atStop < 0) {
                    atStop = frames;
                }
                left--;
                state = left == 0 ? 0 : 3;
                break;
            default:
                left--;
                state = left == 0 ? 0 : 3;
                break;
            }
        }
    }

    /** I frames so far. */
    synchronized int frames() {
        return frames;
    }

    /** I frames between STARTDT con and the end of the time the connection was started; end < 0: its STOPDT con. */
    synchronized int startedFrames(int end) {
        if (atStart < 0) {
            return 0;
        }
        int last = end >= 0 ? end : atStop >= 0 ? atStop : frames;
        return last - atStart;
    }

    @Override
    public Socket createSocket() throws IOException {
        return new TapSocket(inner.createSocket());
    }

    @Override
    public Socket createSocket(String host, int port) {
        throw new UnsupportedOperationException("j60870 connects the socket itself");
    }

    @Override
    public Socket createSocket(String host, int port, InetAddress local, int localPort) {
        throw new UnsupportedOperationException("j60870 connects the socket itself");
    }

    @Override
    public Socket createSocket(InetAddress host, int port) {
        throw new UnsupportedOperationException("j60870 connects the socket itself");
    }

    @Override
    public Socket createSocket(InetAddress host, int port, InetAddress local, int localPort) {
        throw new UnsupportedOperationException("j60870 connects the socket itself");
    }

    /** The socket of the inner factory, TLS or not; what Connection uses of a socket goes to it. */
    private final class TapSocket extends Socket {
        private final Socket socket;

        TapSocket(Socket socket) {
            this.socket = socket;
        }

        @Override
        public void connect(SocketAddress endpoint, int timeout) throws IOException {
            socket.connect(endpoint, timeout);
        }

        @Override
        public void connect(SocketAddress endpoint) throws IOException {
            socket.connect(endpoint);
        }

        @Override
        public void bind(SocketAddress local) throws IOException {
            socket.bind(local);
        }

        @Override
        public InputStream getInputStream() throws IOException {
            return new FilterInputStream(socket.getInputStream()) {
                @Override
                public int read() throws IOException {
                    int octet = super.read();
                    if (octet >= 0) {
                        feed(new byte[] { (byte) octet }, 0, 1);
                    }
                    return octet;
                }

                @Override
                public int read(byte[] b, int off, int len) throws IOException {
                    int n = super.read(b, off, len);
                    if (n > 0) {
                        feed(b, off, n);
                    }
                    return n;
                }
            };
        }

        @Override
        public OutputStream getOutputStream() throws IOException {
            return socket.getOutputStream();
        }

        @Override
        public void setSoTimeout(int timeout) throws java.net.SocketException {
            socket.setSoTimeout(timeout);
        }

        @Override
        public void close() throws IOException {
            socket.close();
        }

        @Override
        public InetAddress getInetAddress() {
            return socket.getInetAddress();
        }

        @Override
        public InetAddress getLocalAddress() {
            return socket.getLocalAddress();
        }

        @Override
        public SocketAddress getRemoteSocketAddress() {
            return socket.getRemoteSocketAddress();
        }

        @Override
        public SocketAddress getLocalSocketAddress() {
            return socket.getLocalSocketAddress();
        }

        @Override
        public int getPort() {
            return socket.getPort();
        }

        @Override
        public int getLocalPort() {
            return socket.getLocalPort();
        }
    }
}
