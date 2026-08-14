package example;

public final class App {
    public static void main(String[] args) throws Exception {
        System.out.println("java");
        Thread.currentThread().join();
    }
}
