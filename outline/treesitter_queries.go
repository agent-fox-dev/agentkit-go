//go:build cgo

package outline

// Tags queries, one per grammar. See grammar for the capture names.

const pythonQuery = `
(function_definition name: (identifier) @name) @definition.func
(class_definition name: (identifier) @name) @definition.class
(module (expression_statement (assignment left: (identifier) @name) @definition.var))
`

const jsQuery = `
(function_declaration name: (identifier) @name) @definition.func
(generator_function_declaration name: (identifier) @name) @definition.func
(class_declaration name: (_) @name) @definition.class
(method_definition name: (_) @name) @definition.method
(variable_declarator name: (identifier) @name value: [(arrow_function) (function_expression)]) @definition.func
`

const tsQuery = jsQuery + `
(function_signature name: (identifier) @name) @definition.func
(abstract_class_declaration name: (_) @name) @definition.class
(abstract_method_signature name: (_) @name) @definition.method
(interface_declaration name: (_) @name) @definition.interface
(enum_declaration name: (_) @name) @definition.enum
(type_alias_declaration name: (_) @name) @definition.type
(internal_module name: (_) @name) @definition.module
`

const javaQuery = `
(class_declaration name: (identifier) @name) @definition.class
(record_declaration name: (identifier) @name) @definition.class
(interface_declaration name: (identifier) @name) @definition.interface
(annotation_type_declaration name: (identifier) @name) @definition.interface
(enum_declaration name: (identifier) @name) @definition.enum
(method_declaration name: (identifier) @name) @definition.method
`

const csharpQuery = `
(namespace_declaration name: (_) @name) @definition.module
(file_scoped_namespace_declaration name: (_) @name) @definition.module
(class_declaration name: (identifier) @name) @definition.class
(record_declaration name: (identifier) @name) @definition.class
(struct_declaration name: (identifier) @name) @definition.type
(interface_declaration name: (identifier) @name) @definition.interface
(enum_declaration name: (identifier) @name) @definition.enum
(delegate_declaration name: (identifier) @name) @definition.type
(method_declaration name: (identifier) @name) @definition.method
`

const kotlinQuery = `
(class_declaration "interface" name: (_) @name) @definition.interface
(class_declaration (modifiers (class_modifier "enum")) name: (_) @name) @definition.enum
(class_declaration name: (_) @name) @definition.class
(object_declaration name: (_) @name) @definition.class
(function_declaration name: (_) @name) @definition.func
(type_alias type: (_) @name) @definition.type
`

const scalaQuery = `
(class_definition name: (_) @name) @definition.class
(object_definition name: (_) @name) @definition.class
(trait_definition name: (_) @name) @definition.trait
(enum_definition name: (_) @name) @definition.enum
(function_definition name: (_) @name) @definition.func
(function_declaration name: (_) @name) @definition.func
(type_definition name: (_) @name) @definition.type
`

const rustQuery = `
(function_item name: (identifier) @name) @definition.func
(function_signature_item name: (identifier) @name) @definition.func
(struct_item name: (_) @name) @definition.type
(union_item name: (_) @name) @definition.type
(type_item name: (_) @name) @definition.type
(enum_item name: (_) @name) @definition.enum
(trait_item name: (_) @name) @definition.trait
(const_item name: (_) @name) @definition.const
(static_item name: (_) @name) @definition.var
(mod_item name: (_) @name) @definition.module
(macro_definition name: (_) @name) @definition.macro
(impl_item type: [(type_identifier) @name
                  (generic_type type: (type_identifier) @name)
                  (scoped_type_identifier name: (type_identifier) @name)]) @scope
`

const cQuery = `
(function_definition declarator: (function_declarator declarator: (identifier) @name)) @definition.func
(function_definition declarator: (pointer_declarator declarator: (function_declarator declarator: (identifier) @name))) @definition.func
(struct_specifier name: (_) @name body: (_)) @definition.type
(union_specifier name: (_) @name body: (_)) @definition.type
(enum_specifier name: (_) @name body: (_)) @definition.enum
(type_definition declarator: (type_identifier) @name) @definition.type
(type_definition declarator: (pointer_declarator declarator: (type_identifier) @name)) @definition.type
(type_definition declarator: (function_declarator declarator: (parenthesized_declarator (pointer_declarator declarator: (type_identifier) @name)))) @definition.type
(preproc_def name: (identifier) @name) @definition.macro
(preproc_function_def name: (identifier) @name) @definition.macro
`

const cppQuery = cQuery + `
(function_definition declarator: (function_declarator declarator: (field_identifier) @name)) @definition.method
(function_definition declarator: (function_declarator declarator: (qualified_identifier scope: (_) @container name: (_) @name))) @definition.method
(function_definition declarator: (reference_declarator (function_declarator declarator: (qualified_identifier scope: (_) @container name: (_) @name)))) @definition.method
(function_definition declarator: (pointer_declarator declarator: (function_declarator declarator: (qualified_identifier scope: (_) @container name: (_) @name)))) @definition.method
(class_specifier name: (_) @name body: (_)) @definition.class
(namespace_definition name: (_) @name) @definition.module
(alias_declaration name: (_) @name) @definition.type
`

const phpQuery = `
(function_definition name: (name) @name) @definition.func
(class_declaration name: (name) @name) @definition.class
(interface_declaration name: (name) @name) @definition.interface
(trait_declaration name: (name) @name) @definition.trait
(enum_declaration name: (name) @name) @definition.enum
(method_declaration name: (name) @name) @definition.method
(namespace_definition name: (namespace_name) @name) @definition.module
`

const rubyQuery = `
(method name: (_) @name) @definition.method
(singleton_method name: (_) @name) @definition.method
(class name: (_) @name) @definition.class
(module name: (_) @name) @definition.module
`

const luaQuery = `
(function_declaration name: (identifier) @name) @definition.func
(function_declaration name: (dot_index_expression table: (_) @container field: (identifier) @name)) @definition.method
(function_declaration name: (method_index_expression table: (_) @container method: (identifier) @name)) @definition.method
`

const bashQuery = `
(function_definition name: (word) @name) @definition.func
`
