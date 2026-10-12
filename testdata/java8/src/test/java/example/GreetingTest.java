package example;

import org.junit.jupiter.api.Test;
import static org.junit.jupiter.api.Assertions.assertEquals;

public class GreetingTest {
    @Test
    public void greetsByName() {
        assertEquals("Hello, Java", Greeting.hello("Java"));
    }
}
