public fun publicFun(): Int {
    return 1
}

fun packageFun(): Int {
    return 2
}

public class PublicClass {
    fun method() {}
}

class PackageClass {
    fun method() {}
}

public interface PublicInterface {
    fun doSomething()
}

public enum class Direction {
    UP, DOWN
}

private fun privateFun(): Int {
    return 3
}

internal class InternalClass {
    fun method() {}
}
