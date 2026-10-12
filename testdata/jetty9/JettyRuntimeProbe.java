import java.nio.file.Path;
import org.eclipse.jetty.annotations.AnnotationConfiguration;
import org.eclipse.jetty.server.LocalConnector;
import org.eclipse.jetty.server.Server;
import org.eclipse.jetty.webapp.*;

/** Runs the real Jetty annotation scanner and an HTTP request without sockets. */
public class JettyRuntimeProbe {
    public static void main(String[] args) throws Exception {
        Path root = Path.of(args[0]);
        Server server = new Server();
        LocalConnector connector = new LocalConnector(server);
        server.addConnector(connector);
        WebAppContext context = new WebAppContext();
        context.setContextPath("/demo");
        context.setResourceBase(root.resolve("src/main/webapp").toString());
        context.setDescriptor(root.resolve("src/main/webapp/WEB-INF/web.xml").toString());
        context.setExtraClasspath(root.resolve("target/classes").toString());
        context.setConfigurations(new Configuration[] {
            new WebInfConfiguration(), new WebXmlConfiguration(),
            new MetaInfConfiguration(), new FragmentConfiguration(),
            new AnnotationConfiguration(), new JettyWebXmlConfiguration()
        });
        server.setHandler(context);
        try {
            server.start();
            if (!context.isAvailable()) {
                throw new IllegalStateException("Webapp unavailable", context.getUnavailableException());
            }
            String response = connector.getResponse(
                "GET /demo/health HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n");
            if (!response.startsWith("HTTP/1.1 200") || !response.contains("Java 21 servlet healthy")) {
                throw new IllegalStateException(response);
            }
            System.out.println("JETTY_PROBE_OK: annotated servlet; context available; HTTP 200");
        } finally {
            server.stop();
            server.join();
        }
    }
}
