package example;

import javax.websocket.OnMessage;
import javax.websocket.server.ServerEndpoint;

@ServerEndpoint("/echo")
public class EchoEndpoint {
    @OnMessage
    public String echo(String message) {
        return message;
    }
}
