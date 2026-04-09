package io.dagger.groovy.transform

import org.codehaus.groovy.ast.ASTNode
import org.codehaus.groovy.ast.AnnotationNode
import org.codehaus.groovy.ast.ClassHelper
import org.codehaus.groovy.ast.ClassNode
import org.codehaus.groovy.ast.ConstructorNode
import org.codehaus.groovy.ast.FieldNode
import org.codehaus.groovy.ast.MethodNode
import org.codehaus.groovy.ast.ModuleNode
import org.codehaus.groovy.ast.Parameter
import org.codehaus.groovy.ast.expr.ConstantExpression
import org.codehaus.groovy.ast.expr.ListExpression
import org.codehaus.groovy.ast.expr.PropertyExpression
import org.codehaus.groovy.control.CompilePhase
import org.codehaus.groovy.control.SourceUnit
import org.codehaus.groovy.transform.ASTTransformation
import org.codehaus.groovy.transform.GroovyASTTransformation

import java.lang.reflect.Modifier

/**
 * Global AST transformation that scans Groovy classes for Dagger annotations
 * ({@code @Object}, {@code @Function}, {@code @Enum}) and generates an
 * {@code Entrypoint.java} file that the Java SDK runtime can execute.
 */
@GroovyASTTransformation(phase = CompilePhase.SEMANTIC_ANALYSIS)
class DaggerModuleASTTransformation implements ASTTransformation {

    private static final String OBJECT_ANNOTATION = 'io.dagger.groovy.annotation.Object'
    private static final String FUNCTION_ANNOTATION = 'io.dagger.groovy.annotation.Function'
    private static final String ENUM_ANNOTATION = 'io.dagger.groovy.annotation.Enum'
    private static final String MODULE_ANNOTATION = 'io.dagger.groovy.annotation.Module'
    private static final String DEFAULT_ANNOTATION = 'io.dagger.groovy.annotation.Default'
    private static final String DEFAULT_PATH_ANNOTATION = 'io.dagger.groovy.annotation.DefaultPath'
    private static final String IGNORE_ANNOTATION = 'io.dagger.groovy.annotation.Ignore'

    @Override
    void visit(ASTNode[] nodes, SourceUnit source) {
        // Only run inside Dagger
        String moduleName = System.getenv('_DAGGER_GROOVY_SDK_MODULE_NAME')
        if (moduleName == null || moduleName.isEmpty()) {
            return
        }

        ModuleNode moduleNode = source.AST
        if (moduleNode == null) {
            return
        }

        List<ClassNode> classes = moduleNode.classes
        if (classes == null || classes.isEmpty()) {
            return
        }

        // Collect annotated objects and enums
        List<DaggerEntrypointGenerator.ObjectMeta> objects = []
        List<DaggerEntrypointGenerator.EnumMeta> enums = []
        Set<String> enumQualifiedNames = new HashSet<>()
        String moduleDescription = ''

        // First pass: collect enums and check for @Module annotation
        for (ClassNode classNode : classes) {
            // Check for @Module annotation on class level
            AnnotationNode moduleAnnotation = findAnnotation(classNode, MODULE_ANNOTATION)
            if (moduleAnnotation != null) {
                String desc = getAnnotationStringValue(moduleAnnotation, 'description')
                if (desc != null && !desc.isEmpty()) {
                    moduleDescription = desc
                }
            }

            // Check for @Enum annotation
            if (classNode.isEnum() && findAnnotation(classNode, ENUM_ANNOTATION) != null) {
                DaggerEntrypointGenerator.EnumMeta enumMeta = collectEnumMeta(classNode)
                if (enumMeta != null) {
                    enums.add(enumMeta)
                    enumQualifiedNames.add(classNode.name)
                }
            }
        }

        // Set known enums before processing objects
        DaggerTypeMapper.setKnownEnums(enumQualifiedNames)

        // Second pass: collect objects
        for (ClassNode classNode : classes) {
            if (classNode.isEnum()) continue

            AnnotationNode objectAnnotation = findAnnotation(classNode, OBJECT_ANNOTATION)
            if (objectAnnotation == null) continue

            DaggerEntrypointGenerator.ObjectMeta obj = collectObjectMeta(classNode, objectAnnotation, moduleName)
            if (obj != null) {
                objects.add(obj)

                // If no module description yet, try groovydoc on main object
                if (moduleDescription.isEmpty() && obj.isMainObject) {
                    // groovydoc is not easily accessible during SEMANTIC_ANALYSIS
                    // description from @Object value is used
                }
            }
        }

        if (objects.isEmpty()) {
            return
        }

        // Generate and write Entrypoint.java
        String entrypointSource = DaggerEntrypointGenerator.generate(moduleDescription, objects, enums)

        // Use absolute path from env var (set by Go runtime), fall back to relative
        String generatedDir = System.getenv('_DAGGER_GROOVY_GENERATED_DIR')
            ?: System.getProperty('dagger.groovy.generated.dir', 'build/generated/sources/dagger')
        File outputFile = new File(generatedDir, 'io/dagger/gen/entrypoint/Entrypoint.java')
        outputFile.parentFile.mkdirs()
        outputFile.text = entrypointSource
    }

    /**
     * Collects metadata from an @Object-annotated class.
     */
    private DaggerEntrypointGenerator.ObjectMeta collectObjectMeta(ClassNode classNode,
                                                                     AnnotationNode objectAnnotation,
                                                                     String moduleName) {
        DaggerEntrypointGenerator.ObjectMeta obj = new DaggerEntrypointGenerator.ObjectMeta()

        // Name: from @Object value or class simple name
        String annotationName = getAnnotationStringValue(objectAnnotation, 'value')
        obj.name = (annotationName != null && !annotationName.isEmpty()) ? annotationName : classNode.nameWithoutPackage
        obj.qualifiedName = classNode.name
        obj.description = getAnnotationStringValue(objectAnnotation, 'value') ?: ''
        // If the annotation value was used as the name, description should come from groovydoc
        // For now, use the groovydoc comment if available
        String groovyDoc = extractGroovyDoc(classNode)
        if (groovyDoc != null && !groovyDoc.isEmpty()) {
            obj.description = groovyDoc
        } else {
            // Don't use annotation value as description if it was used as name override
            if (annotationName != null && !annotationName.isEmpty()) {
                obj.description = ''
            }
        }

        obj.isMainObject = areSimilar(obj.name, moduleName)

        // Collect @Function-annotated methods
        obj.functions = collectFunctions(classNode)

        // Collect fields: public, non-static, non-transient, non-final; or @Function-annotated
        obj.fields = collectFields(classNode)

        // Collect constructor for main object
        if (obj.isMainObject) {
            obj.constructor = collectConstructor(classNode)
        }

        return obj
    }

    /**
     * Collects @Function-annotated methods from a class.
     */
    private List<DaggerEntrypointGenerator.FunctionMeta> collectFunctions(ClassNode classNode) {
        List<DaggerEntrypointGenerator.FunctionMeta> functions = []

        for (MethodNode method : classNode.methods) {
            AnnotationNode fnAnnotation = findAnnotation(method, FUNCTION_ANNOTATION)
            if (fnAnnotation == null) continue

            // Validate: method must be public
            if (!Modifier.isPublic(method.modifiers)) {
                throw new RuntimeException(
                    "The method ${classNode.name}#${method.name} must be public if annotated with @Function")
            }

            // Validate: must have explicit return type (not java.lang.Object from missing declaration)
            ClassNode returnType = method.returnType
            if (returnType.name == 'java.lang.Object' && !isExplicitObjectReturn(method)) {
                throw new RuntimeException(
                    "The method ${classNode.name}#${method.name} must declare an explicit return type. " +
                    "Groovy's default 'def' return type is not supported by Dagger.")
            }

            DaggerEntrypointGenerator.FunctionMeta fn = new DaggerEntrypointGenerator.FunctionMeta()

            // Function name: from annotation value or method name
            String annotationName = getAnnotationStringValue(fnAnnotation, 'value')
            fn.name = (annotationName != null && !annotationName.isEmpty()) ? annotationName : method.name
            fn.methodName = method.name
            fn.returnType = resolveTypeName(returnType)

            // Description from groovydoc
            fn.description = extractGroovyDoc(method) ?: ''

            // Parameters
            fn.parameters = collectParameters(method)

            functions.add(fn)
        }

        return functions
    }

    /**
     * Collects exposed fields from a class.
     */
    private List<DaggerEntrypointGenerator.FieldMeta> collectFields(ClassNode classNode) {
        List<DaggerEntrypointGenerator.FieldMeta> fields = []

        for (FieldNode field : classNode.fields) {
            // Skip synthetic fields
            if (field.isSynthetic()) continue

            // Skip static, transient, final
            if (Modifier.isStatic(field.modifiers)) continue
            if (Modifier.isTransient(field.modifiers)) continue
            if (Modifier.isFinal(field.modifiers)) continue

            // Include if public or has @Function annotation
            boolean isPublic = Modifier.isPublic(field.modifiers)
            boolean hasFunctionAnnotation = findAnnotation(field, FUNCTION_ANNOTATION) != null

            if (!isPublic && !hasFunctionAnnotation) continue

            DaggerEntrypointGenerator.FieldMeta fieldMeta = new DaggerEntrypointGenerator.FieldMeta()
            fieldMeta.name = field.name
            fieldMeta.type = resolveTypeName(field.type)
            fieldMeta.description = extractGroovyDoc(field) ?: ''

            fields.add(fieldMeta)
        }

        return fields
    }

    /**
     * Collects constructor metadata for the main object.
     * Only non-empty constructors are considered (the no-arg constructor is always the default).
     */
    private DaggerEntrypointGenerator.ConstructorMeta collectConstructor(ClassNode classNode) {
        List<ConstructorNode> constructors = classNode.declaredConstructors

        // Filter to non-empty public constructors
        List<ConstructorNode> nonEmpty = constructors.findAll { ctor ->
            ctor.parameters.length > 0
        }

        if (nonEmpty.isEmpty()) {
            return null
        }

        if (nonEmpty.size() > 1) {
            throw new RuntimeException(
                "The class ${classNode.name} must have a single non-empty constructor")
        }

        ConstructorNode ctor = nonEmpty[0]
        DaggerEntrypointGenerator.ConstructorMeta meta = new DaggerEntrypointGenerator.ConstructorMeta()
        meta.description = extractGroovyDoc(ctor) ?: ''
        meta.parameters = []

        for (Parameter param : ctor.parameters) {
            // Skip Client parameters
            if (param.type.name == 'io.dagger.client.Client') continue

            DaggerEntrypointGenerator.ParameterMeta pmeta = new DaggerEntrypointGenerator.ParameterMeta()
            pmeta.name = param.name

            String typeName = resolveTypeName(param.type)

            // Handle Optional<X>
            if (typeName.startsWith('java.util.Optional<')) {
                pmeta.optional = true
                typeName = typeName.substring('java.util.Optional<'.length(), typeName.length() - 1)
            }

            pmeta.type = typeName

            // Check for @Default annotation
            AnnotationNode defaultAnnotation = findAnnotation(param, DEFAULT_ANNOTATION)
            if (defaultAnnotation != null) {
                String defaultVal = getAnnotationStringValue(defaultAnnotation, 'value')
                if (defaultVal != null) {
                    pmeta.defaultValue = quoteIfString(defaultVal, typeName)
                }
            }

            // Check for @DefaultPath annotation
            AnnotationNode defaultPathAnnotation = findAnnotation(param, DEFAULT_PATH_ANNOTATION)
            if (defaultPathAnnotation != null) {
                pmeta.defaultPath = getAnnotationStringValue(defaultPathAnnotation, 'value')
            }

            // Check for @Ignore annotation
            AnnotationNode ignoreAnnotation = findAnnotation(param, IGNORE_ANNOTATION)
            if (ignoreAnnotation != null) {
                pmeta.ignore = getAnnotationStringArrayValue(ignoreAnnotation, 'value')
            }

            // Description from GroovyDoc @param tags
            pmeta.description = extractParamDescription(ctor, param.name) ?: ''

            meta.parameters.add(pmeta)
        }

        return meta
    }

    /**
     * Collects parameter metadata from a method.
     */
    private List<DaggerEntrypointGenerator.ParameterMeta> collectParameters(MethodNode method) {
        List<DaggerEntrypointGenerator.ParameterMeta> params = []

        for (Parameter param : method.parameters) {
            // Skip Client parameters
            if (param.type.name == 'io.dagger.client.Client') continue

            DaggerEntrypointGenerator.ParameterMeta pmeta = new DaggerEntrypointGenerator.ParameterMeta()
            pmeta.name = param.name

            String typeName = resolveTypeName(param.type)

            // Handle Optional<X>
            if (typeName.startsWith('java.util.Optional<')) {
                pmeta.optional = true
                typeName = typeName.substring('java.util.Optional<'.length(), typeName.length() - 1)
            }

            pmeta.type = typeName

            // Check for @Default annotation
            AnnotationNode defaultAnnotation = findAnnotation(param, DEFAULT_ANNOTATION)
            if (defaultAnnotation != null) {
                String defaultVal = getAnnotationStringValue(defaultAnnotation, 'value')
                if (defaultVal != null) {
                    pmeta.defaultValue = quoteIfString(defaultVal, typeName)
                }
            }

            // Check for @DefaultPath annotation
            AnnotationNode defaultPathAnnotation = findAnnotation(param, DEFAULT_PATH_ANNOTATION)
            if (defaultPathAnnotation != null) {
                pmeta.defaultPath = getAnnotationStringValue(defaultPathAnnotation, 'value')
            }

            // Check for @Ignore annotation
            AnnotationNode ignoreAnnotation = findAnnotation(param, IGNORE_ANNOTATION)
            if (ignoreAnnotation != null) {
                pmeta.ignore = getAnnotationStringArrayValue(ignoreAnnotation, 'value')
            }

            // Description from GroovyDoc @param tags
            pmeta.description = extractParamDescription(method, param.name) ?: ''

            params.add(pmeta)
        }

        return params
    }

    /**
     * Collects metadata from an @Enum-annotated enum class.
     */
    private DaggerEntrypointGenerator.EnumMeta collectEnumMeta(ClassNode classNode) {
        DaggerEntrypointGenerator.EnumMeta enumMeta = new DaggerEntrypointGenerator.EnumMeta()
        enumMeta.name = classNode.nameWithoutPackage
        enumMeta.description = extractGroovyDoc(classNode) ?: ''
        enumMeta.values = []

        for (FieldNode field : classNode.fields) {
            if (field.isEnum()) {
                DaggerEntrypointGenerator.EnumValueMeta valueMeta = new DaggerEntrypointGenerator.EnumValueMeta()
                valueMeta.value = field.name
                valueMeta.description = extractGroovyDoc(field) ?: ''
                enumMeta.values.add(valueMeta)
            }
        }

        return enumMeta
    }

    // ---- Utility methods ----

    /**
     * Resolves a ClassNode to a fully qualified type name string.
     */
    private static String resolveTypeName(ClassNode classNode) {
        if (classNode == null || classNode == ClassHelper.VOID_TYPE) {
            return 'void'
        }

        // Handle arrays
        if (classNode.isArray()) {
            return "${resolveTypeName(classNode.componentType)}[]"
        }

        // Handle generics (e.g. List<String>, Optional<Directory>)
        if (classNode.genericsTypes != null && classNode.genericsTypes.length > 0) {
            String baseName = classNode.name
            String[] genericArgs = classNode.genericsTypes.collect { gt ->
                gt.type != null ? resolveTypeName(gt.type) : 'java.lang.Object'
            }
            return "${baseName}<${genericArgs.join(', ')}>"
        }

        // Handle primitives
        if (classNode == ClassHelper.boolean_TYPE) return 'boolean'
        if (classNode == ClassHelper.int_TYPE) return 'int'
        if (classNode == ClassHelper.long_TYPE) return 'long'
        if (classNode == ClassHelper.short_TYPE) return 'short'
        if (classNode == ClassHelper.byte_TYPE) return 'byte'
        if (classNode == ClassHelper.float_TYPE) return 'float'
        if (classNode == ClassHelper.double_TYPE) return 'double'
        if (classNode == ClassHelper.char_TYPE) return 'char'

        return classNode.name
    }

    /**
     * Checks if a method explicitly returns java.lang.Object (vs. using 'def' or omitting type).
     * During SEMANTIC_ANALYSIS, Groovy AST marks untyped methods as returning Object.
     * We check if there's no explicit return type annotation, which means the user used 'def'.
     */
    private static boolean isExplicitObjectReturn(MethodNode method) {
        // In the Groovy AST, if the method was declared as 'def methodName()',
        // the return type is set to java.lang.Object, same as 'Object methodName()'.
        // We check the original source to distinguish, but during AST transformation
        // we can check if the return type ClassNode is exactly the OBJECT_TYPE placeholder.
        // Groovy sets isDynamicTyped on the method when 'def' is used.
        return method.isDynamicReturnType() ? false : true
    }

    /**
     * Finds an annotation on a node by its fully qualified class name.
     */
    private static AnnotationNode findAnnotation(ASTNode node, String annotationClassName) {
        List<AnnotationNode> annotations
        if (node instanceof ClassNode) {
            annotations = ((ClassNode) node).annotations
        } else if (node instanceof MethodNode) {
            annotations = ((MethodNode) node).annotations
        } else if (node instanceof FieldNode) {
            annotations = ((FieldNode) node).annotations
        } else if (node instanceof ConstructorNode) {
            annotations = ((ConstructorNode) node).annotations
        } else if (node instanceof Parameter) {
            annotations = ((Parameter) node).annotations
        } else {
            return null
        }

        // Extract simple name for fallback matching (during SEMANTIC_ANALYSIS
        // the annotation ClassNode may not have its fully qualified name resolved yet)
        String simpleName = annotationClassName.substring(annotationClassName.lastIndexOf('.') + 1)

        for (AnnotationNode ann : annotations) {
            String annName = ann.classNode.name
            if (annName == annotationClassName || annName == simpleName) {
                return ann
            }
        }
        return null
    }

    /**
     * Gets a string value from an annotation member.
     */
    private static String getAnnotationStringValue(AnnotationNode annotation, String memberName) {
        def expr = annotation.getMember(memberName)
        if (expr instanceof ConstantExpression) {
            Object val = ((ConstantExpression) expr).value
            return val != null ? val.toString() : null
        }
        return null
    }

    /**
     * Gets a string array value from an annotation member.
     */
    private static String[] getAnnotationStringArrayValue(AnnotationNode annotation, String memberName) {
        def expr = annotation.getMember(memberName)
        if (expr instanceof ListExpression) {
            ListExpression listExpr = (ListExpression) expr
            return listExpr.expressions.collect { e ->
                if (e instanceof ConstantExpression) {
                    return ((ConstantExpression) e).value?.toString()
                }
                return e.text
            }.toArray(new String[0])
        }
        if (expr instanceof ConstantExpression) {
            return [((ConstantExpression) expr).value?.toString()] as String[]
        }
        return null
    }

    /**
     * Extracts GroovyDoc description from a class or method node.
     * During AST transformation, GroovyDoc is available as a comment string.
     */
    private static String extractGroovyDoc(ASTNode node) {
        // In Groovy AST, groovydoc is stored differently depending on version.
        // Try to get it from the node's groovydoc property
        try {
            def groovydoc = node.groovydoc
            if (groovydoc != null && groovydoc.isPresent()) {
                String content = groovydoc.get().content
                if (content != null) {
                    return parseGroovyDocDescription(content)
                }
            }
        } catch (Exception ignored) {
            // groovydoc API might not be available
        }
        return ''
    }

    /**
     * Parses the description part of a GroovyDoc comment (before any @tags).
     */
    private static String parseGroovyDocDescription(String groovyDoc) {
        if (groovyDoc == null) return ''

        // Remove /** and */
        String content = groovyDoc
        if (content.startsWith('/**')) {
            content = content.substring(3)
        }
        if (content.endsWith('*/')) {
            content = content.substring(0, content.length() - 2)
        }

        // Split into lines, remove leading asterisks and whitespace
        String[] lines = content.split('\n')
        StringBuilder sb = new StringBuilder()
        for (String line : lines) {
            String trimmed = line.trim()
            if (trimmed.startsWith('*')) {
                trimmed = trimmed.substring(1).trim()
            }
            // Stop at @tags
            if (trimmed.startsWith('@')) break
            if (sb.length() > 0 && !trimmed.isEmpty()) {
                sb.append('\n')
            }
            sb.append(trimmed)
        }

        return sb.toString().trim()
    }

    /**
     * Extracts a @param description from a method's GroovyDoc.
     */
    private static String extractParamDescription(ASTNode methodNode, String paramName) {
        try {
            def groovydoc = methodNode.groovydoc
            if (groovydoc != null && groovydoc.isPresent()) {
                String content = groovydoc.get().content
                if (content != null) {
                    return parseParamDescription(content, paramName)
                }
            }
        } catch (Exception ignored) {
            // groovydoc API might not be available
        }
        return ''
    }

    /**
     * Parses a @param tag from GroovyDoc.
     */
    private static String parseParamDescription(String groovyDoc, String paramName) {
        if (groovyDoc == null) return ''

        // Look for @param paramName description
        String[] lines = groovyDoc.split('\n')
        StringBuilder desc = new StringBuilder()
        boolean found = false

        for (String line : lines) {
            String trimmed = line.trim()
            if (trimmed.startsWith('*')) {
                trimmed = trimmed.substring(1).trim()
            }
            if (trimmed.startsWith("@param ${paramName}")) {
                found = true
                String remainder = trimmed.substring("@param ${paramName}".length()).trim()
                desc.append(remainder)
            } else if (found) {
                // Continue collecting multi-line param description
                if (trimmed.startsWith('@') || trimmed.isEmpty()) break
                desc.append(' ').append(trimmed)
            }
        }

        return desc.toString().trim()
    }

    /**
     * Determines if two names are "similar" using the same normalization as the Java SDK.
     * Splits camelCase, replaces hyphens/underscores, lowercases, removes spaces.
     */
    static boolean areSimilar(String str1, String str2) {
        return normalize(str1) == normalize(str2)
    }

    private static String normalize(String str) {
        if (str == null) return ''
        return str
            .replaceAll('[-_]', ' ')                    // Replace kebab and snake case delimiters with spaces
            .replaceAll('([a-z])([A-Z])', '$1 $2')      // Split camel case words
            .toLowerCase(Locale.ROOT)                     // Convert to lowercase
            .replaceAll('\\s+', '')                       // Remove all spaces
    }

    /**
     * Quotes a default value if it's a string type, following the Java SDK convention.
     */
    private static String quoteIfString(String value, String type) {
        if (value == null) return null
        if ((type == 'java.lang.String' || type == 'String')
            && value != 'null'
            && !(value.startsWith('"') && value.endsWith('"'))
            && !(value.startsWith("'") && value.endsWith("'"))) {
            return '"' + value.replaceAll('"', '\\\\"') + '"'
        }
        return value
    }
}
