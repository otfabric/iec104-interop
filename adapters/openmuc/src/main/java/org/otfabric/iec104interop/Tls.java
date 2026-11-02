// SPDX-License-Identifier: GPL-3.0-or-later
package org.otfabric.iec104interop;

import java.io.ByteArrayInputStream;
import java.io.IOException;
import java.net.InetAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.AlgorithmParameters;
import java.security.GeneralSecurityException;
import java.security.KeyFactory;
import java.security.KeyStore;
import java.security.Signature;
import java.security.cert.Certificate;
import java.security.cert.CertificateFactory;
import java.security.interfaces.ECPrivateKey;
import java.security.spec.ECGenParameterSpec;
import java.security.spec.ECParameterSpec;
import java.security.spec.PKCS8EncodedKeySpec;
import java.util.ArrayList;
import java.util.Base64;
import java.util.List;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

import javax.net.ServerSocketFactory;
import javax.net.SocketFactory;
import javax.net.ssl.KeyManagerFactory;
import javax.net.ssl.SSLContext;
import javax.net.ssl.SSLServerSocket;
import javax.net.ssl.SSLSocket;
import javax.net.ssl.SSLSocketFactory;
import javax.net.ssl.TrustManagerFactory;

/**
 * The transport security flags of the contract: mutual TLS, with key material the JDK loads from PEM files. A host name
 * is never checked: JSSE checks one only when it is given an endpoint identification algorithm, and it is not.
 */
final class Tls {
    private static final Pattern PEM = Pattern.compile("-----BEGIN ([A-Z0-9 ]+)-----(.*?)-----END \\1-----",
            Pattern.DOTALL);
    private static final String[] PROTOCOLS = { "TLSv1.3", "TLSv1.2" };

    private final SSLContext context;

    private Tls(SSLContext context) {
        this.context = context;
    }

    /**
     * Consumes the flags. Returns null without --tls; anything wrong with them is a usage error, before anything is
     * sent.
     */
    static Tls fromArgs(Args args) {
        boolean on = args.flag("--tls");
        String cert = args.string("--tls-cert", null);
        String key = args.string("--tls-key", null);
        String trust = args.string("--tls-trust", null);
        if (!on) {
            if (cert != null || key != null || trust != null) {
                throw new Args.UsageException("--tls-cert, --tls-key and --tls-trust need --tls");
            }
            return null;
        }
        if (cert == null || key == null || trust == null) {
            throw new Args.UsageException("--tls needs --tls-cert, --tls-key and --tls-trust");
        }
        try {
            return new Tls(context(cert, key, trust));
        } catch (IOException | GeneralSecurityException | IllegalArgumentException e) {
            throw new Args.UsageException("--tls: cannot load the key material: " + e.getMessage());
        }
    }

    private static List<byte[]> pem(String path, String label) throws IOException {
        String text = Files.readString(Path.of(path), StandardCharsets.UTF_8);
        List<byte[]> blocks = new ArrayList<>();
        Matcher m = PEM.matcher(text);
        while (m.find()) {
            if (m.group(1).equals(label)) {
                blocks.add(Base64.getMimeDecoder().decode(m.group(2)));
            }
        }
        if (blocks.isEmpty()) {
            throw new IOException(path + " has no " + label);
        }
        return blocks;
    }

    private static Certificate[] certificates(String path) throws IOException, GeneralSecurityException {
        CertificateFactory factory = CertificateFactory.getInstance("X.509");
        List<Certificate> certs = new ArrayList<>();
        for (byte[] der : pem(path, "CERTIFICATE")) {
            certs.add(factory.generateCertificate(new ByteArrayInputStream(der)));
        }
        return certs.toArray(new Certificate[0]);
    }

    private static ECPrivateKey key(String path) throws IOException, GeneralSecurityException {
        byte[] der = pem(path, "PRIVATE KEY").get(0);
        ECPrivateKey key = (ECPrivateKey) KeyFactory.getInstance("EC").generatePrivate(new PKCS8EncodedKeySpec(der));
        AlgorithmParameters p = AlgorithmParameters.getInstance("EC");
        p.init(new ECGenParameterSpec("secp256r1"));
        if (!p.getParameterSpec(ECParameterSpec.class).getOrder().equals(key.getParams().getOrder())) {
            throw new GeneralSecurityException(path + " is not on secp256r1");
        }
        return key;
    }

    private static SSLContext context(String certPath, String keyPath, String trustPath)
            throws IOException, GeneralSecurityException {
        Certificate[] chain = certificates(certPath);
        ECPrivateKey key = key(keyPath);

        byte[] probe = { 1, 0, 4 };
        Signature s = Signature.getInstance("SHA256withECDSA");
        s.initSign(key);
        s.update(probe);
        byte[] signature = s.sign();
        s.initVerify(chain[0].getPublicKey());
        s.update(probe);
        if (!s.verify(signature)) {
            throw new GeneralSecurityException(keyPath + " is not the key of " + certPath);
        }

        char[] password = new char[0];
        KeyStore own = KeyStore.getInstance(KeyStore.getDefaultType());
        own.load(null, null);
        own.setKeyEntry("own", key, password, chain);
        KeyManagerFactory kmf = KeyManagerFactory.getInstance(KeyManagerFactory.getDefaultAlgorithm());
        kmf.init(own, password);

        KeyStore anchors = KeyStore.getInstance(KeyStore.getDefaultType());
        anchors.load(null, null);
        int n = 0;
        for (Certificate c : certificates(trustPath)) {
            anchors.setCertificateEntry("authority-" + n++, c);
        }
        TrustManagerFactory tmf = TrustManagerFactory.getInstance("PKIX");
        tmf.init(anchors);

        SSLContext context = SSLContext.getInstance("TLS");
        context.init(kmf.getKeyManagers(), tmf.getTrustManagers(), null);
        return context;
    }

    /**
     * For ClientConnectionBuilder. The socket shakes hands on the first read or write after connecting, so a handshake
     * that fails or hangs is something that happens to STARTDT.
     */
    SocketFactory clientFactory() {
        SSLSocketFactory inner = context.getSocketFactory();
        return new SocketFactory() {
            private Socket versions(Socket s) {
                ((SSLSocket) s).setEnabledProtocols(PROTOCOLS);
                return s;
            }

            @Override
            public Socket createSocket() throws IOException {
                return versions(inner.createSocket());
            }

            @Override
            public Socket createSocket(String host, int port) throws IOException {
                return versions(inner.createSocket(host, port));
            }

            @Override
            public Socket createSocket(String host, int port, InetAddress local, int localPort) throws IOException {
                return versions(inner.createSocket(host, port, local, localPort));
            }

            @Override
            public Socket createSocket(InetAddress host, int port) throws IOException {
                return versions(inner.createSocket(host, port));
            }

            @Override
            public Socket createSocket(InetAddress host, int port, InetAddress local, int localPort)
                    throws IOException {
                return versions(inner.createSocket(host, port, local, localPort));
            }
        };
    }

    /**
     * For Server.Builder. j60870 makes the server socket with the factory it is given, so this is the one place to ask
     * for the certificate of the client.
     */
    ServerSocketFactory serverFactory() {
        ServerSocketFactory inner = context.getServerSocketFactory();
        return new ServerSocketFactory() {
            private ServerSocket mutual(ServerSocket s) {
                SSLServerSocket ssl = (SSLServerSocket) s;
                ssl.setNeedClientAuth(true);
                ssl.setEnabledProtocols(PROTOCOLS);
                return ssl;
            }

            @Override
            public ServerSocket createServerSocket() throws IOException {
                return mutual(inner.createServerSocket());
            }

            @Override
            public ServerSocket createServerSocket(int port) throws IOException {
                return mutual(inner.createServerSocket(port));
            }

            @Override
            public ServerSocket createServerSocket(int port, int backlog) throws IOException {
                return mutual(inner.createServerSocket(port, backlog));
            }

            @Override
            public ServerSocket createServerSocket(int port, int backlog, InetAddress address) throws IOException {
                return mutual(inner.createServerSocket(port, backlog, address));
            }
        };
    }
}
