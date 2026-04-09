package io.dagger.groovy.transform

/**
 * Maps Groovy/Java type names to Dagger TypeDef code strings and class literals.
 *
 * This mirrors the logic from the Java annotation processor's DaggerType class
 * but generates string code snippets instead of JavaPoet CodeBlocks.
 */
class DaggerTypeMapper {

    /** Set of fully qualified enum type names known to the module */
    static Set<String> knownEnums = new HashSet<>()

    static void setKnownEnums(Set<String> enums) {
        knownEnums = enums
    }

    /**
     * Returns a code string like {@code dag().typeDef().withKind(TypeDefKind.STRING_KIND)}
     * or {@code dag().typeDef().withObject("Container")}.
     */
    static String toDaggerTypeDef(String typeName) {
        if (typeName == null) {
            return 'dag().typeDef().withKind(TypeDefKind.VOID_KIND).withOptional(true)'
        }

        // Check known enums first
        if (knownEnums.contains(typeName)) {
            String simpleName = typeName.substring(typeName.lastIndexOf('.') + 1)
            return "dag().typeDef().withEnum(\"${simpleName}\")"
        }

        switch (typeName) {
            case 'void':
                return 'dag().typeDef().withKind(TypeDefKind.VOID_KIND).withOptional(true)'
            case 'boolean':
            case 'java.lang.Boolean':
                return 'dag().typeDef().withKind(TypeDefKind.BOOLEAN_KIND)'
            case 'int':
            case 'long':
            case 'short':
            case 'byte':
            case 'java.lang.Integer':
            case 'java.lang.Long':
            case 'java.lang.Short':
            case 'java.lang.Byte':
                return 'dag().typeDef().withKind(TypeDefKind.INTEGER_KIND)'
            case 'float':
            case 'double':
            case 'java.lang.Float':
            case 'java.lang.Double':
                return 'dag().typeDef().withKind(TypeDefKind.FLOAT_KIND)'
            case 'java.lang.String':
            case 'String':
                return 'dag().typeDef().withKind(TypeDefKind.STRING_KIND)'
        }

        // List types: java.util.List<X>
        if (typeName.startsWith('java.util.List<')) {
            String inner = typeName.substring('java.util.List<'.length(), typeName.length() - 1)
            return "dag().typeDef().withListOf(${toDaggerTypeDef(inner)})"
        }

        // Array types: X[]
        if (typeName.endsWith('[]')) {
            String inner = typeName.substring(0, typeName.length() - 2)
            return "dag().typeDef().withListOf(${toDaggerTypeDef(inner)})"
        }

        // Optional types: java.util.Optional<X>
        if (typeName.startsWith('java.util.Optional<')) {
            String inner = typeName.substring('java.util.Optional<'.length(), typeName.length() - 1)
            return toDaggerTypeDef(inner)
        }

        // Try to detect enums and scalars via reflection
        try {
            Class<?> clazz = Class.forName(typeName)
            if (clazz.isEnum()) {
                String simpleName = typeName.substring(typeName.lastIndexOf('.') + 1)
                return "dag().typeDef().withEnum(\"${simpleName}\")"
            }
            if (io.dagger.client.Scalar.isAssignableFrom(clazz)) {
                String simpleName = typeName.substring(typeName.lastIndexOf('.') + 1)
                return "dag().typeDef().withScalar(\"${simpleName}\")"
            }
        } catch (ClassNotFoundException ignored) {
            // not ideal, but we only want to know if it's an enum or a Scalar
        }

        // Check java.lang boxed types that map to TypeDefKind
        if (typeName.startsWith('java.lang.')) {
            String simpleName = typeName.substring(typeName.lastIndexOf('.') + 1)
            try {
                io.dagger.client.TypeDefKind.valueOf("${simpleName.toUpperCase()}_KIND")
                return "dag().typeDef().withKind(TypeDefKind.${simpleName.toUpperCase()}_KIND)"
            } catch (IllegalArgumentException ignored) {
                // valueOf failed - not a kind
            }
        }

        // Default: treat as Dagger object
        String simpleName = typeName.substring(typeName.lastIndexOf('.') + 1)
        return "dag().typeDef().withObject(\"${simpleName}\")"
    }

    /**
     * Returns a class literal string like {@code String.class} or {@code Container.class}.
     * For list types, returns the array class (e.g. {@code String[].class}).
     */
    static String toClassLiteral(String typeName) {
        if (typeName == null || typeName == 'void') {
            return 'Void.class'
        }

        // Known enums
        if (knownEnums.contains(typeName)) {
            return "${typeName}.class"
        }

        switch (typeName) {
            case 'boolean': return 'boolean.class'
            case 'int': return 'int.class'
            case 'long': return 'long.class'
            case 'short': return 'short.class'
            case 'byte': return 'byte.class'
            case 'float': return 'float.class'
            case 'double': return 'double.class'
            case 'java.lang.Boolean': return 'Boolean.class'
            case 'java.lang.Integer': return 'Integer.class'
            case 'java.lang.Long': return 'Long.class'
            case 'java.lang.Short': return 'Short.class'
            case 'java.lang.Byte': return 'Byte.class'
            case 'java.lang.Float': return 'Float.class'
            case 'java.lang.Double': return 'Double.class'
            case 'java.lang.String':
            case 'String': return 'String.class'
        }

        // List types: for deserialization, use array class
        if (typeName.startsWith('java.util.List<')) {
            String inner = typeName.substring('java.util.List<'.length(), typeName.length() - 1)
            return "${toArrayClassLiteral(inner)}"
        }

        // Array types
        if (typeName.endsWith('[]')) {
            String inner = typeName.substring(0, typeName.length() - 2)
            return "${toSimpleClassName(inner)}[].class"
        }

        // Optional types
        if (typeName.startsWith('java.util.Optional<')) {
            String inner = typeName.substring('java.util.Optional<'.length(), typeName.length() - 1)
            return toClassLiteral(inner)
        }

        // Default: use simple name
        return "${toSimpleClassName(typeName)}.class"
    }

    /**
     * Returns the simple Java type expression for use in variable declarations.
     * For example: "String", "Container", "int", "boolean".
     */
    static String toJavaType(String typeName) {
        if (typeName == null || typeName == 'void') {
            return 'void'
        }

        if (knownEnums.contains(typeName)) {
            return typeName
        }

        switch (typeName) {
            case 'boolean': return 'boolean'
            case 'int': return 'int'
            case 'long': return 'long'
            case 'short': return 'short'
            case 'byte': return 'byte'
            case 'float': return 'float'
            case 'double': return 'double'
            case 'java.lang.Boolean': return 'Boolean'
            case 'java.lang.Integer': return 'Integer'
            case 'java.lang.Long': return 'Long'
            case 'java.lang.Short': return 'Short'
            case 'java.lang.Byte': return 'Byte'
            case 'java.lang.Float': return 'Float'
            case 'java.lang.Double': return 'Double'
            case 'java.lang.String':
            case 'String': return 'String'
        }

        if (typeName.startsWith('java.util.List<')) {
            String inner = typeName.substring('java.util.List<'.length(), typeName.length() - 1)
            return "List<${toJavaType(inner)}>"
        }

        if (typeName.endsWith('[]')) {
            String inner = typeName.substring(0, typeName.length() - 2)
            return "${toJavaType(inner)}[]"
        }

        if (typeName.startsWith('java.util.Optional<')) {
            String inner = typeName.substring('java.util.Optional<'.length(), typeName.length() - 1)
            return toJavaType(inner)
        }

        return toSimpleClassName(typeName)
    }

    /** Returns true if the type is a List or array type */
    static boolean isList(String typeName) {
        return typeName != null && (typeName.startsWith('java.util.List<') || typeName.endsWith('[]'))
    }

    /** Returns true if the type is a primitive type */
    static boolean isPrimitive(String typeName) {
        return typeName in ['boolean', 'int', 'long', 'short', 'byte', 'float', 'double', 'char']
    }

    /** Returns the default value for a type: "null" for objects, "0" for numbers, "false" for boolean */
    static String defaultValue(String typeName) {
        switch (typeName) {
            case 'boolean': return 'false'
            case 'int':
            case 'long':
            case 'short':
            case 'byte':
            case 'float':
            case 'double':
            case 'char':
                return '0'
            default:
                return 'null'
        }
    }

    /** Extracts the simple class name from a fully qualified name */
    private static String toSimpleClassName(String qualifiedName) {
        return qualifiedName.substring(qualifiedName.lastIndexOf('.') + 1)
    }

    /** Returns array class literal for list inner types, e.g. "String[].class" */
    private static String toArrayClassLiteral(String innerType) {
        switch (innerType) {
            case 'java.lang.Integer':
            case 'Integer': return 'Integer[].class'
            case 'java.lang.Long':
            case 'Long': return 'Long[].class'
            case 'java.lang.String':
            case 'String': return 'String[].class'
            case 'java.lang.Boolean':
            case 'Boolean': return 'Boolean[].class'
            case 'java.lang.Float':
            case 'Float': return 'Float[].class'
            case 'java.lang.Double':
            case 'Double': return 'Double[].class'
        }
        return "${toSimpleClassName(innerType)}[].class"
    }
}
