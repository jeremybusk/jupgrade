package example;

import org.junit.jupiter.api.Test;
import static org.junit.jupiter.api.Assertions.assertEquals;

public class PersonTest {
    @Test
    public void generatesGetter() {
        assertEquals("Java", new Person("Java").getName());
    }
}
