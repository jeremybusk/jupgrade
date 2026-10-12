package example;

import lombok.Getter;

public final class Person {
    @Getter
    private final String name;

    public Person(String name) {
        this.name = name;
    }
}
