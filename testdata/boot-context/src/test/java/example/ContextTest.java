package example;

import org.junit.jupiter.api.Test;
import org.springframework.boot.SpringBootVersion;
import org.springframework.boot.WebApplicationType;
import org.springframework.boot.builder.SpringApplicationBuilder;
import org.springframework.context.annotation.Configuration;

import static org.junit.jupiter.api.Assertions.assertTrue;

class ContextTest {
    @Configuration
    static class Config { }

    @Test
    void starts() {
        try (var context = new SpringApplicationBuilder(Config.class)
                .web(WebApplicationType.NONE)
                .logStartupInfo(false)
                .properties("spring.main.banner-mode=off")
                .run()) {
            assertTrue(context.isActive());
            System.out.println("BOOT_CONTEXT_VERSION=" + SpringBootVersion.getVersion());
        }
    }
}
