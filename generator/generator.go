package generator

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"regexp"
	"strings"
)

func GenerateFake(structName, packageName string, interfaceNode *ast.InterfaceType) (string, error) {
	buf := new(bytes.Buffer)
	err := printer.Fprint(
		buf,
		token.NewFileSet(),
		sourceFile(structName, packageName, interfaceNode),
	)
	return prettifyCode(buf.String()), err
}

func sourceFile(structName, packageName string, interfaceNode *ast.InterfaceType) ast.Node {
	declarations := []ast.Decl{
		importsDecl(),
		typeDecl(structName, interfaceNode),
	}

	for _, method := range interfaceNode.Methods.List {
		methodType := method.Type.(*ast.FuncType)

		declarations = append(
			declarations,
			methodImplementationDecl(structName, method),
			methodCallCountGetterDecl(structName, method),
			methodCallArgsGetterDecl(structName, method),
		)

		if methodType.Results != nil {
			declarations = append(
				declarations,
				methodReturnsSetterDecl(structName, method),
			)
		}
	}

	return &ast.File{
		Name:  &ast.Ident{Name: packageName},
		Decls: declarations,
	}
}

func importsDecl() ast.Decl {
	return &ast.GenDecl{
		Tok: token.IMPORT,
		Specs: []ast.Spec{&ast.ImportSpec{
			Path: &ast.BasicLit{
				Kind:  token.STRING,
				Value: `"sync"`,
			},
		}},
	}
}

func typeDecl(structName string, iface *ast.InterfaceType) ast.Decl {
	structFields := []*ast.Field{
		{
			Type: &ast.SelectorExpr{
				X:   ast.NewIdent("sync"),
				Sel: ast.NewIdent("RWMutex"),
			},
		},
	}

	for _, method := range iface.Methods.List {
		methodType := method.Type.(*ast.FuncType)

		structFields = append(
			structFields,

			&ast.Field{
				Names: []*ast.Ident{ast.NewIdent(methodStubFuncName(method))},
				Type:  method.Type,
			},

			&ast.Field{
				Names: []*ast.Ident{ast.NewIdent(callArgsFieldName(method))},
				Type: &ast.ArrayType{
					Elt: argsStructTypeForMethod(methodType),
				},
			},
		)

		if methodType.Results != nil {
			structFields = append(
				structFields,
				&ast.Field{
					Names: []*ast.Ident{ast.NewIdent(returnStructFieldName(method))},
					Type:  returnStructTypeForMethod(methodType),
				},
			)
		}
	}

	return &ast.GenDecl{
		Tok: token.TYPE,
		Specs: []ast.Spec{
			&ast.TypeSpec{
				Name: &ast.Ident{Name: structName},
				Type: &ast.StructType{
					Fields: &ast.FieldList{
						List: structFields,
					},
				},
			},
		},
	}
}

func methodImplementationDecl(structName string, method *ast.Field) *ast.FuncDecl {
	methodType := method.Type.(*ast.FuncType)

	stubFunc := &ast.SelectorExpr{
		X:   receiverIdent(),
		Sel: ast.NewIdent(methodStubFuncName(method)),
	}

	paramValues := []ast.Expr{}
	paramFields := []*ast.Field{}
	var ellipsisPos token.Pos

	for i, field := range methodType.Params.List {
		paramValues = append(paramValues, ast.NewIdent(nameForMethodParam(i)))

		paramFields = append(paramFields, &ast.Field{
			Names: []*ast.Ident{ast.NewIdent(nameForMethodParam(i))},
			Type:  field.Type,
		})

		if _, ok := field.Type.(*ast.Ellipsis); ok {
			ellipsisPos = token.Pos(i)
		}
	}

	stubFuncCall := &ast.CallExpr{
		Fun:      stubFunc,
		Args:     paramValues,
		Ellipsis: ellipsisPos,
	}

	var lastStatement ast.Stmt
	if methodType.Results != nil {
		returnValues := []ast.Expr{}
		for i, _ := range methodType.Results.List {
			returnValues = append(returnValues, &ast.SelectorExpr{
				X: &ast.SelectorExpr{
					X:   receiverIdent(),
					Sel: ast.NewIdent(returnStructFieldName(method)),
				},
				Sel: ast.NewIdent(nameForMethodResult(i)),
			})
		}

		lastStatement = &ast.IfStmt{
			Cond: nilCheck(stubFunc),
			Body: &ast.BlockStmt{List: []ast.Stmt{
				&ast.ReturnStmt{Results: []ast.Expr{stubFuncCall}},
			}},
			Else: &ast.BlockStmt{List: []ast.Stmt{
				&ast.ReturnStmt{Results: returnValues},
			}},
		}
	} else {
		lastStatement = &ast.IfStmt{
			Cond: nilCheck(stubFunc),
			Body: &ast.BlockStmt{List: []ast.Stmt{
				&ast.ExprStmt{X: stubFuncCall},
			}},
		}
	}

	return &ast.FuncDecl{
		Name: method.Names[0],
		Type: &ast.FuncType{
			Params:  &ast.FieldList{List: paramFields},
			Results: methodType.Results,
		},
		Recv: receiverFieldList(structName),
		Body: &ast.BlockStmt{
			List: []ast.Stmt{
				&ast.ExprStmt{
					X: &ast.CallExpr{
						Fun: &ast.SelectorExpr{
							X:   receiverIdent(),
							Sel: ast.NewIdent("Lock"),
						},
					},
				},
				&ast.DeferStmt{
					Call: &ast.CallExpr{
						Fun: &ast.SelectorExpr{
							X:   receiverIdent(),
							Sel: ast.NewIdent("Unlock"),
						},
					},
				},
				&ast.AssignStmt{
					Tok: token.ASSIGN,
					Lhs: []ast.Expr{&ast.SelectorExpr{
						X:   receiverIdent(),
						Sel: ast.NewIdent(callArgsFieldName(method)),
					}},
					Rhs: []ast.Expr{&ast.CallExpr{
						Fun: ast.NewIdent("append"),
						Args: []ast.Expr{
							&ast.SelectorExpr{
								X:   receiverIdent(),
								Sel: ast.NewIdent(callArgsFieldName(method)),
							},
							&ast.CompositeLit{
								Type: argsStructTypeForMethod(methodType),
								Elts: paramValues,
							},
						},
					}},
				},
				lastStatement,
			},
		},
	}
}

func methodCallCountGetterDecl(structName string, method *ast.Field) *ast.FuncDecl {
	return &ast.FuncDecl{
		Name: ast.NewIdent(callCountMethodName(method)),
		Type: &ast.FuncType{
			Results: &ast.FieldList{List: []*ast.Field{
				&ast.Field{
					Type: ast.NewIdent("int"),
				},
			}},
		},
		Recv: receiverFieldList(structName),
		Body: &ast.BlockStmt{
			List: []ast.Stmt{
				&ast.ExprStmt{
					X: &ast.CallExpr{
						Fun: &ast.SelectorExpr{
							X:   receiverIdent(),
							Sel: ast.NewIdent("RLock"),
						},
					},
				},
				&ast.DeferStmt{
					Call: &ast.CallExpr{
						Fun: &ast.SelectorExpr{
							X:   receiverIdent(),
							Sel: ast.NewIdent("RUnlock"),
						},
					},
				},
				&ast.ReturnStmt{
					Results: []ast.Expr{
						&ast.CallExpr{
							Fun: ast.NewIdent("len"),
							Args: []ast.Expr{
								&ast.SelectorExpr{
									X:   receiverIdent(),
									Sel: ast.NewIdent(callArgsFieldName(method)),
								},
							},
						},
					},
				},
			},
		},
	}
}

func methodCallArgsGetterDecl(structName string, method *ast.Field) *ast.FuncDecl {
	indexIdent := ast.NewIdent("i")

	resultValues := []ast.Expr{}
	resultTypes := []*ast.Field{}

	for i, field := range method.Type.(*ast.FuncType).Params.List {
		resultValues = append(resultValues, &ast.SelectorExpr{
			X: &ast.IndexExpr{
				X: &ast.SelectorExpr{
					X:   receiverIdent(),
					Sel: ast.NewIdent(callArgsFieldName(method)),
				},
				Index: indexIdent,
			},
			Sel: ast.NewIdent(nameForMethodParam(i)),
		})

		resultTypes = append(resultTypes, &ast.Field{
			Type: storedTypeForType(field.Type),
		})
	}

	return &ast.FuncDecl{
		Name: ast.NewIdent(callArgsMethodName(method)),
		Type: &ast.FuncType{
			Params: &ast.FieldList{List: []*ast.Field{
				&ast.Field{
					Names: []*ast.Ident{indexIdent},
					Type:  ast.NewIdent("int"),
				},
			}},
			Results: &ast.FieldList{List: resultTypes},
		},
		Recv: receiverFieldList(structName),
		Body: &ast.BlockStmt{
			List: []ast.Stmt{
				&ast.ExprStmt{
					X: &ast.CallExpr{
						Fun: &ast.SelectorExpr{
							X:   receiverIdent(),
							Sel: ast.NewIdent("RLock"),
						},
					},
				},
				&ast.DeferStmt{
					Call: &ast.CallExpr{
						Fun: &ast.SelectorExpr{
							X:   receiverIdent(),
							Sel: ast.NewIdent("RUnlock"),
						},
					},
				},
				&ast.ReturnStmt{
					Results: resultValues,
				},
			},
		},
	}
}

func methodReturnsSetterDecl(structName string, method *ast.Field) *ast.FuncDecl {
	methodType := method.Type.(*ast.FuncType)

	params := []*ast.Field{}
	structFields := []ast.Expr{}
	for i, result := range methodType.Results.List {
		params = append(params, &ast.Field{
			Names: []*ast.Ident{ast.NewIdent(nameForMethodResult(i))},
			Type:  result.Type,
		})

		structFields = append(structFields, ast.NewIdent(nameForMethodResult(i)))
	}

	return &ast.FuncDecl{
		Name: ast.NewIdent(returnSetterMethodName(method)),
		Type: &ast.FuncType{
			Params: &ast.FieldList{List: params},
		},
		Recv: receiverFieldList(structName),
		Body: &ast.BlockStmt{
			List: []ast.Stmt{
				&ast.AssignStmt{
					Tok: token.ASSIGN,
					Lhs: []ast.Expr{
						&ast.SelectorExpr{
							X:   receiverIdent(),
							Sel: ast.NewIdent(returnStructFieldName(method)),
						},
					},
					Rhs: []ast.Expr{
						&ast.CompositeLit{
							Type: returnStructTypeForMethod(methodType),
							Elts: structFields,
						},
					},
				},
			},
		},
	}
}

func argsStructTypeForMethod(methodType *ast.FuncType) *ast.StructType {
	fields := []*ast.Field{}
	for i, field := range methodType.Params.List {
		fields = append(fields, &ast.Field{
			Type:  storedTypeForType(field.Type),
			Names: []*ast.Ident{ast.NewIdent(nameForMethodParam(i))},
		})
	}

	return &ast.StructType{
		Fields: &ast.FieldList{List: fields},
	}
}

func returnStructTypeForMethod(methodType *ast.FuncType) *ast.StructType {
	resultFields := []*ast.Field{}
	for i, field := range methodType.Results.List {
		resultFields = append(resultFields, &ast.Field{
			Type:  field.Type,
			Names: []*ast.Ident{ast.NewIdent(nameForMethodResult(i))},
		})
	}

	return &ast.StructType{
		Fields: &ast.FieldList{List: resultFields},
	}
}

func storedTypeForType(t ast.Expr) ast.Expr {
	if ellipsis, ok := t.(*ast.Ellipsis); ok {
		return &ast.ArrayType{Elt: ellipsis.Elt}
	} else {
		return t
	}
}

func nameForMethodResult(i int) string {
	return fmt.Sprintf("result%d", i+1)
}

func nameForMethodParam(i int) string {
	return fmt.Sprintf("arg%d", i+1)
}

func callCountMethodName(method *ast.Field) string {
	return method.Names[0].Name + "CallCount"
}

func callArgsMethodName(method *ast.Field) string {
	return method.Names[0].Name + "ArgsForCall"
}

func callArgsFieldName(method *ast.Field) string {
	return privatize(callArgsMethodName(method))
}

func methodStubFuncName(method *ast.Field) string {
	return method.Names[0].Name + "Stub"
}

func returnSetterMethodName(method *ast.Field) string {
	return method.Names[0].Name + "Returns"
}

func returnStructFieldName(method *ast.Field) string {
	return privatize(returnSetterMethodName(method))
}

func receiverIdent() *ast.Ident {
	return ast.NewIdent("fake")
}

func receiverFieldList(structName string) *ast.FieldList {
	return &ast.FieldList{
		List: []*ast.Field{
			{
				Names: []*ast.Ident{receiverIdent()},
				Type:  &ast.StarExpr{X: ast.NewIdent(structName)},
			},
		},
	}
}

func publicize(input string) string {
	return strings.ToUpper(input[0:1]) + input[1:]
}

func privatize(input string) string {
	return strings.ToLower(input[0:1]) + input[1:]
}

func nilCheck(x ast.Expr) ast.Expr {
	return &ast.BinaryExpr{
		X:  x,
		Op: token.NEQ,
		Y: &ast.BasicLit{
			Kind:  token.STRING,
			Value: "nil",
		},
	}
}

var funcRegexp = regexp.MustCompile("\n(func)")

func prettifyCode(code string) string {
	code = funcRegexp.ReplaceAllString(code, "\n\n$1")
	code = strings.Replace(code, "\n\n\n", "\n\n", -1)
	return code
}