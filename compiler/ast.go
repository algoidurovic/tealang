package compiler

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

type literalDesc struct {
	offset  uint
	theType exprType
}

type literalInfo struct {
	literals map[string]literalDesc

	intc  []string
	bytec [][]byte
}

type context struct {
	name         string
	literals     *literalInfo
	parent       *context
	vars         map[string]varInfo
	functions    map[string]*funCallNode
	addressEntry uint // first address to use on the context creation
	addressNext  uint // next address to use
}

type varKind int

const (
	constantKind varKind = 1
	functionKind varKind = 2
)

type callDefParser func(context *context, callNode *funCallNode, varInfo *varInfo) *funDefNode

type varInfo struct {
	name    string
	theType exprType
	kind    varKind

	// for variables specifies allocated memory scratch space
	// for constants sets index in intc/bytec arrays
	address uint

	// constants have value
	value *string

	// function has reference lazy parser
	parser callDefParser
	node   TreeNodeIf
}

func (v varInfo) constant() bool {
	return v.kind == constantKind
}

func (v varInfo) function() bool {
	return v.kind == functionKind
}

func newLiteralInfo() (literals *literalInfo) {
	literals = new(literalInfo)
	literals.literals = make(map[string]literalDesc)
	literals.intc = make([]string, 0, 128)
	literals.bytec = make([][]byte, 0, 128)
	return
}

func newContext(name string, parent *context) (ctx *context) {
	ctx = new(context)
	ctx.name = name
	ctx.parent = parent
	ctx.vars = make(map[string]varInfo)
	ctx.functions = make(map[string]*funCallNode)
	if parent != nil {
		ctx.literals = parent.literals
		ctx.addressEntry = parent.addressNext
		ctx.addressNext = ctx.addressEntry
	} else {
		ctx.literals = newLiteralInfo()
		ctx.addressEntry = 0
		ctx.addressNext = 0

		// global context, add internal literals
		ctx.addLiteral(falseConstValue, intType)
		ctx.addLiteral(trueConstValue, intType)
	}
	return
}

func (ctx *context) lookup(name string) (varable varInfo, err error) {
	current := ctx
	for current != nil {
		variable, ok := current.vars[name]
		if ok {
			return variable, nil
		}
		current = current.parent
	}
	return varInfo{}, fmt.Errorf("ident '%s' not defined", name)
}

func (ctx *context) update(name string, info varInfo) (err error) {
	current := ctx
	for current != nil {
		_, ok := current.vars[name]
		if ok {
			current.vars[name] = info
			return nil
		}
		current = current.parent
	}
	return fmt.Errorf("failed to update ident %s", name)
}

// remapTo remaps this context variable addresses by using newBase as a new entry address
func (ctx *context) remapTo(newBase uint) {
	vars := make([]varInfo, 0, len(ctx.vars))
	for _, info := range ctx.vars {
		vars = append(vars, info)
	}
	sort.Slice(vars, func(i, j int) bool { return vars[i].address < vars[j].address })
	ctx.addressEntry = newBase
	for _, info := range vars {
		info.address = newBase
		ctx.update(info.name, info)
		newBase++
	}
	ctx.addressNext = newBase
}

func (ctx *context) newVar(name string, theType exprType) error {
	if _, ok := ctx.vars[name]; ok {
		return fmt.Errorf("variable '%s' already declared", name)
	}
	ctx.vars[name] = varInfo{name, theType, 0, ctx.addressNext, nil, nil, nil}
	ctx.addressNext++
	return nil
}

func (ctx *context) newConst(name string, theType exprType, value *string) error {
	if _, ok := ctx.vars[name]; ok {
		return fmt.Errorf("const '%s' already declared", name)
	}
	offset, err := ctx.addLiteral(*value, theType)
	if err != nil {
		return err
	}
	ctx.vars[name] = varInfo{name, theType, constantKind, offset, value, nil, nil}
	return nil
}

func (ctx *context) newFunc(name string, theType exprType, parser callDefParser) error {
	if _, ok := ctx.vars[name]; ok {
		return fmt.Errorf("function '%s' already defined", name)
	}

	ctx.vars[name] = varInfo{name, theType, functionKind, 0, nil, parser, nil}
	return nil
}

func (ctx *context) addLiteral(value string, theType exprType) (offset uint, err error) {
	info, exists := ctx.literals.literals[value]
	if !exists {
		if theType == intType {
			offset = uint(len(ctx.literals.intc))
			ctx.literals.intc = append(ctx.literals.intc, value)
			ctx.literals.literals[value] = literalDesc{offset, intType}
		} else if theType == bytesType {
			offset = uint(len(ctx.literals.bytec))
			parsed, err := parseStringLiteral(value)
			if err != nil {
				return 0, err
			}
			ctx.literals.bytec = append(ctx.literals.bytec, parsed)
			ctx.literals.literals[value] = literalDesc{offset, bytesType}
		} else {
			return 0, fmt.Errorf("unknown literal type %s (%s)", theType, value)
		}
	} else {
		offset = info.offset
	}

	return offset, err
}

func (ctx *context) Print() {
	for name, value := range ctx.vars {
		fmt.Printf("%v %v\n", name, value)
	}
}

func (ctx *context) EntryAddress() uint {
	return ctx.addressEntry
}

func (ctx *context) LastAddress() uint {
	return ctx.addressNext
}

type exprType int

const (
	unknownType exprType = 0
	intType     exprType = 1
	bytesType   exprType = 2
	invalidType exprType = 99
)

func (n exprType) String() string {
	switch n {
	case intType:
		return "uint64"
	case bytesType:
		return "byte[]"
	case invalidType:
		return "invalid"
	}
	return "unknown"
}

// TreeNodeIf represents a node in AST
type TreeNodeIf interface {
	append(ch TreeNodeIf)
	children() []TreeNodeIf
	parent() TreeNodeIf
	String() string
	Print()
	Codegen(ostream io.Writer)
}

// ExprNodeIf extends TreeNode and can be evaluated and typed
type ExprNodeIf interface {
	TreeNodeIf
	getType() (exprType, error)
}

// TreeNode contains base info about an AST node
type TreeNode struct {
	ctx *context

	nodeName      string
	parentNode    TreeNodeIf
	childrenNodes []TreeNodeIf
}

type programNode struct {
	*TreeNode
	nonInlineFunc []*funDefNode
}

type funArg struct {
	n string
	t exprType
}

type funDefNode struct {
	*TreeNode
	name   string
	args   []funArg
	inline bool
}

type blockNode struct {
	*TreeNode
}

type returnNode struct {
	*TreeNode
	value      ExprNodeIf
	definition *funDefNode
}

type errorNode struct {
	*TreeNode
}

type itxnBeginNode struct {
	*TreeNode
}

type itxnEndNode struct {
	*TreeNode
}

type assignInnerTxnNode struct {
	*TreeNode
	name     string
	exprType exprType
	value    ExprNodeIf
}

type breakNode struct {
	*TreeNode
	value ExprNodeIf
}

type assignNode struct {
	*TreeNode
	name     string
	exprType exprType
	value    ExprNodeIf
}

type assignTupleNode struct {
	*TreeNode
	low      string
	high     string
	exprType exprType
	value    ExprNodeIf
}

type assignQuadrupleNode struct {
	*TreeNode
	low      string
	high     string
	rlow     string
	rhigh    string
	exprType exprType
	value    ExprNodeIf
}

type varDeclNode struct {
	*TreeNode
	name     string
	exprType exprType
	value    ExprNodeIf
}

type varDeclTupleNode struct {
	*TreeNode
	low      string
	high     string
	exprType exprType
	value    ExprNodeIf
}

type varDeclQuadrupleNode struct {
	*TreeNode
	low      string
	high     string
	rlow     string
	rhigh    string
	exprType exprType
	value    ExprNodeIf
}

type constNode struct {
	*TreeNode
	name     string
	exprType exprType
	value    string
}

type exprIdentNode struct {
	*TreeNode
	exprType exprType
	name     string
}

type exprLiteralNode struct {
	*TreeNode
	exprType exprType
	value    string
}

type exprBinOpNode struct {
	*TreeNode
	exprType exprType
	op       string
	lhs      ExprNodeIf
	rhs      ExprNodeIf
}

type exprGroupNode struct {
	*TreeNode
	value ExprNodeIf
}

type exprUnOpNode struct {
	*TreeNode
	op    string
	value ExprNodeIf
}

type ifExprNode struct {
	*TreeNode
	condExpr      ExprNodeIf
	condTrueExpr  ExprNodeIf
	condFalseExpr ExprNodeIf
}

type forStatementNode struct {
	*TreeNode
	condExpr     ExprNodeIf
	condTrueExpr ExprNodeIf
}

type ifStatementNode struct {
	*TreeNode
	condExpr ExprNodeIf
}

type typeCastNode struct {
	*TreeNode
	expr       ExprNodeIf
	targetType exprType
}

type funCallNode struct {
	*TreeNode
	name       string
	field      string
	index1     string
	index2     string
	funType    exprType
	definition *funDefNode
}

type runtimeFieldNode struct {
	*TreeNode
	op       string
	field    string
	index1   string
	index2   string
	exprType exprType
}

type runtimeArgNode struct {
	*TreeNode
	op       string
	number   string
	exprType exprType
}

//--------------------------------------------------------------------------------------------------
//
// AST nodes constructors
//
//--------------------------------------------------------------------------------------------------

func newNode(ctx *context, parent TreeNodeIf) (node *TreeNode) {
	node = new(TreeNode)
	node.ctx = ctx
	node.childrenNodes = make([]TreeNodeIf, 0)
	node.parentNode = parent
	return node
}

func newProgramNode(ctx *context, parent TreeNodeIf) (node *programNode) {
	node = new(programNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "program"
	return
}

func newBlockNode(ctx *context, parent TreeNodeIf) (node *blockNode) {
	node = new(blockNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "block"
	return
}

func newReturnNode(ctx *context, parent TreeNodeIf) (node *returnNode) {
	node = new(returnNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "ret"
	node.value = nil
	return
}

func newErorrNode(ctx *context, parent TreeNodeIf) (node *errorNode) {
	node = new(errorNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "error"
	return
}

func newInnertxnBeginNode(ctx *context, parent TreeNodeIf) (node *itxnBeginNode) {
	node = new(itxnBeginNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "begin"
	return
}

func newInnertxnEndNode(ctx *context, parent TreeNodeIf) (node *itxnEndNode) {
	node = new(itxnEndNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "end"
	return
}

func newAssignInnerTxnNode(ctx *context, parent TreeNodeIf, ident string) (node *assignInnerTxnNode) {
	node = new(assignInnerTxnNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "assignItxn"
	node.name = ident
	node.value = nil
	return
}

func newBreakNode(ctx *context, parent TreeNodeIf) (node *breakNode) {
	node = new(breakNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "break"
	node.value = nil
	return
}

func newAssignNode(ctx *context, parent TreeNodeIf, ident string) (node *assignNode) {
	node = new(assignNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "assign"
	node.name = ident
	node.value = nil
	return
}

func newAssignTupleNode(ctx *context, parent TreeNodeIf, identLow string, identHigh string) (node *assignTupleNode) {
	node = new(assignTupleNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "assign tuple"
	node.low = identLow
	node.high = identHigh
	node.value = nil
	return
}

func newAssignQuadrupleNode(ctx *context, parent TreeNodeIf, identLow string, identHigh string, remLow string, remHigh string) (node *assignQuadrupleNode) {
	node = new(assignQuadrupleNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "assign quadruple"
	node.low = identLow
	node.high = identHigh
	node.rlow = remLow
	node.rhigh = remHigh
	node.value = nil
	return
}

func newFunDefNode(ctx *context, parent TreeNodeIf) (node *funDefNode) {
	node = new(funDefNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "func"
	return
}

func newVarDeclNode(ctx *context, parent TreeNodeIf, ident string, value ExprNodeIf) (node *varDeclNode) {
	node = new(varDeclNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "var"
	node.name = ident
	node.value = value
	tp, _ := value.getType()
	node.exprType = tp
	return
}

func newVarDeclTupleNode(ctx *context, parent TreeNodeIf, identLow string, identHigh string, value ExprNodeIf) (node *varDeclTupleNode) {
	node = new(varDeclTupleNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "var, var"
	node.low = identLow
	node.high = identHigh
	node.value = value
	tp, _ := value.getType()
	node.exprType = tp
	return
}

func newVarDeclDivmodwTupleNode(ctx *context, parent TreeNodeIf, identLow string, identHigh string, remLow string, remHigh string, value ExprNodeIf) (node *varDeclQuadrupleNode) {
	node = new(varDeclQuadrupleNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "divmodw"
	node.low = identLow
	node.high = identHigh
	node.rlow = remLow
	node.rhigh = remHigh
	node.value = value
	tp, _ := value.getType()
	node.exprType = tp
	return
}

func newConstNode(ctx *context, parent TreeNodeIf, ident string, value string, exprType exprType) (node *constNode) {
	node = new(constNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "const"
	node.name = ident
	node.value = value
	node.exprType = exprType
	return
}

func newExprIdentNode(ctx *context, parent TreeNodeIf, name string, exprType exprType) (node *exprIdentNode) {
	node = new(exprIdentNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "expr ident"
	node.name = name
	node.exprType = exprType
	return
}

func newExprLiteralNode(ctx *context, parent TreeNodeIf, valType exprType, value string) (node *exprLiteralNode) {
	node = new(exprLiteralNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "expr liter"
	node.value = value
	node.exprType = valType
	return
}

func newExprBinOpNode(ctx *context, parent TreeNodeIf, op string) (node *exprBinOpNode) {
	node = new(exprBinOpNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "expr OP expr"
	node.exprType = intType
	node.op = op
	return
}

func newExprGroupNode(ctx *context, parent TreeNodeIf, value ExprNodeIf) (node *exprGroupNode) {
	node = new(exprGroupNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "(expr)"
	node.value = value
	return
}

func newExprUnOpNode(ctx *context, parent TreeNodeIf, op string) (node *exprUnOpNode) {
	node = new(exprUnOpNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "OP expr"
	node.op = op
	return
}

func newIfExprNode(ctx *context, parent TreeNodeIf) (node *ifExprNode) {
	node = new(ifExprNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "if expr"
	return
}

func newIfStatementNode(ctx *context, parent TreeNodeIf) (node *ifStatementNode) {
	node = new(ifStatementNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "if stmt"
	return
}

func newForStatementNode(ctx *context, parent TreeNodeIf) (node *forStatementNode) {
	node = new(forStatementNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "for stmt"
	return
}

func newFunCallNode(ctx *context, parent TreeNodeIf, name string, aux ...string) (node *funCallNode) {
	node = new(funCallNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "fun call"
	node.name = name
	if len(aux) > 0 {
		node.field = aux[0]
	}
	node.funType = unknownType
	return
}

func newTypeCastExprNode(ctx *context, parent TreeNodeIf, targetType exprType) (node *typeCastNode) {
	node = new(typeCastNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "type cast (" + targetType.String() + ")"
	node.targetType = targetType
	return
}

func newRuntimeFieldNode(ctx *context, parent TreeNodeIf, op string, field string, aux ...string) (node *runtimeFieldNode) {
	node = new(runtimeFieldNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "runtime field"
	node.op = op
	node.field = field
	if len(aux) > 0 {
		node.index1 = aux[0]
	}
	if len(aux) > 1 {
		node.index2 = aux[1]
	}
	node.exprType = unknownType
	return
}

func newRuntimeArgNode(ctx *context, parent TreeNodeIf, op string, number string) (node *runtimeArgNode) {
	node = new(runtimeArgNode)
	node.TreeNode = newNode(ctx, parent)
	node.nodeName = "runtime arg"
	node.op = op
	node.number = number
	node.exprType = unknownType
	return
}

//--------------------------------------------------------------------------------------------------
//
// Type checks
//
//--------------------------------------------------------------------------------------------------

func (n *exprLiteralNode) getType() (exprType, error) {
	return n.exprType, nil
}

func (n *exprIdentNode) getType() (exprType, error) {
	if n.exprType == unknownType {
		info, err := n.ctx.lookup(n.name)
		if err != nil || info.theType == invalidType {
			return invalidType, fmt.Errorf("ident lookup for %s failed: %s", n.name, err.Error())
		}
		n.exprType = info.theType
	}
	return n.exprType, nil
}

func (n *exprBinOpNode) getType() (exprType, error) {
	tp, err := opTypeFromSpec(n.op, 0)
	if err != nil {
		return invalidType, fmt.Errorf("bin op '%s' not it the language: %s", n.op, err.Error())
	}

	lhs, err := n.lhs.getType()
	if err != nil {
		return invalidType, fmt.Errorf("left operand '%s' has invalid type: %s", n.lhs.String(), err.Error())
	}
	rhs, err := n.rhs.getType()
	if err != nil {
		return invalidType, fmt.Errorf("right operand '%s' has invalid type: %s", n.rhs.String(), err.Error())
	}

	opLHS, err := argOpTypeFromSpec(n.op, 0)
	if err != nil {
		return invalidType, err
	}
	if opLHS != unknownType && lhs != opLHS {
		return invalidType, fmt.Errorf("incompatible left operand type: '%s' vs '%s' in expr '%s'", opLHS, lhs, n)
	}

	opRHS, err := argOpTypeFromSpec(n.op, 1)
	if err != nil {
		return invalidType, err
	}
	if opRHS != unknownType && rhs != opRHS {
		return invalidType, fmt.Errorf("incompatible right operand type: '%s' vs '%s' in expr '%s'", opRHS, rhs, n)
	}
	if lhs != rhs {
		return invalidType, fmt.Errorf("incompatible types: '%s' vs '%s' in expr '%s'", lhs, rhs, n)
	}

	return tp, nil
}

func (n *exprUnOpNode) getType() (exprType, error) {
	tp, err := opTypeFromSpec(n.op, 0)
	if err != nil {
		return invalidType, fmt.Errorf("un op '%s' not it the language: %s", n.op, err.Error())
	}

	valType, err := n.value.getType()
	if err != nil {
		return invalidType, fmt.Errorf("operand '%s' has invalid type: %s", n.String(), err.Error())
	}

	operandType, err := argOpTypeFromSpec(n.op, 0)
	if err != nil {
		return invalidType, err
	}
	if operandType != unknownType && valType != operandType {
		return invalidType, fmt.Errorf("incompatible operand type: '%s' vs %s in expr '%s'", operandType, valType, n)
	}

	if tp != valType {
		return invalidType, fmt.Errorf("up op expects type '%s' but operand is '%s'", tp, valType)
	}
	return tp, nil
}

func (n *ifExprNode) getType() (exprType, error) {
	tp, err := n.condExpr.getType()
	if err != nil {
		return invalidType, fmt.Errorf("cond type evaluation failed: %s", err.Error())
	}

	condType := tp
	if condType != intType {
		return invalidType, fmt.Errorf("cond type is '%s', expected '%s'", condType, tp)
	}

	condTrueExprType, err := n.condTrueExpr.getType()
	if err != nil {
		return invalidType, fmt.Errorf("first block has invalid type: %s", err.Error())
	}
	condFalseExprType, err := n.condFalseExpr.getType()
	if err != nil {
		return invalidType, fmt.Errorf("second block has invalid type: %s", err.Error())
	}
	if condTrueExprType != condFalseExprType {
		return invalidType, fmt.Errorf("if blocks types mismatch '%s' vs '%s'", condTrueExprType, condFalseExprType)
	}

	return condTrueExprType, nil
}

func (n *exprGroupNode) getType() (exprType, error) {
	return n.value.getType()
}

// Scans node's children recursively and find return statements,
// applies type resolution and track conflicts.
// Return expr type or invalidType on error
func determineBlockReturnType(node TreeNodeIf, retTypeSeen []exprType) (exprType, error) {
	var statements []TreeNodeIf
	if node != nil {
		statements = node.children()
	}

	for _, stmt := range statements {
		switch tt := stmt.(type) {
		case *returnNode:
			tp, err := tt.value.getType()
			if err != nil {
				return invalidType, err
			}
			retTypeSeen = append(retTypeSeen, tp)
		case *errorNode:
			retTypeSeen = append(retTypeSeen, intType) // error is ok
		case *ifStatementNode, *blockNode:
			blockType, err := determineBlockReturnType(stmt, retTypeSeen)
			if err != nil {
				return invalidType, err
			}
			retTypeSeen = append(retTypeSeen, blockType)
		}
	}

	if len(retTypeSeen) == 0 {
		return unknownType, nil
	}
	commonType := retTypeSeen[0]
	for _, tp := range retTypeSeen {
		if commonType == unknownType && tp != unknownType {
			commonType = tp
			continue
		}

		if commonType != unknownType && tp != commonType {
			return invalidType, fmt.Errorf("block types mismatch: %s vs %s", commonType, tp)
		}
	}
	return commonType, nil
}

func ensureBlockReturns(node TreeNodeIf) bool {
	chLength := len(node.children())
	if chLength == 0 {
		return false
	}

	lastNode := node.children()[chLength-1]
	switch tt := lastNode.(type) {
	case *returnNode, *errorNode:
		return true
	case *ifStatementNode:
		if len(tt.children()) == 1 {
			// only if-block present
			return false
		}
		// otherwise ensure both if-else and else-block returns
		return ensureBlockReturns(lastNode.children()[0]) && ensureBlockReturns(lastNode.children()[1])
	default:
	}

	return false
}

func (n *funCallNode) getType() (exprType, error) {
	if n.funType != unknownType {
		return n.funType, nil
	}

	var err error
	builtin := false
	_, err = n.ctx.lookup(n.name)
	if err != nil {
		_, builtin = builtinFun[n.name]
		if !builtin {
			return invalidType, fmt.Errorf("function %s lookup failed: %s", n.name, err.Error())
		}
	}

	var tp exprType
	if builtin {
		tp, err = opTypeFromSpec(n.name, 0)
		if tp == unknownType {
			if idx, ok := builtinFunDependantTypes[n.name]; ok {
				tp, err = n.childrenNodes[idx].(ExprNodeIf).getType()
				if err != nil {
					return invalidType, fmt.Errorf("function %s type deduction failed: %s", n.name, err.Error())
				}
			}
		}
	} else {
		tp, err = determineBlockReturnType(n.definition, []exprType{})
	}
	n.funType = tp
	return tp, err
}

func (n *funCallNode) getTypeTuple() (exprType, exprType, error) {
	var err error
	builtin := false
	_, builtin = builtinFun[n.name]
	if !builtin {
		return invalidType, invalidType, fmt.Errorf("function %s lookup failed: %s", n.name, err.Error())
	}

	var tpl exprType = invalidType
	var tph exprType = invalidType
	tph, err = opTypeFromSpec(n.name, 0)
	if err != nil {
		return tph, tpl, err
	}
	tpl, err = opTypeFromSpec(n.name, 1)

	// some functions (acct_params_get for example) might have any type in the return spec
	// but also have field types. In this case funCallNode has it resolved and can be used
	if n.funType != unknownType {
		if tph == unknownType {
			tph = n.funType
		} else if tpl == unknownType {
			tpl = n.funType
		}
	}
	return tph, tpl, err
}

func (n *funCallNode) getTypeQuadruple() (exprType, exprType, exprType, exprType, error) {
	var err error
	builtin := false
	_, builtin = builtinFun[n.name]
	if !builtin {
		return invalidType, invalidType, invalidType, invalidType, fmt.Errorf("function %s lookup failed: %s", n.name, err.Error())
	}

	var tpl exprType = invalidType
	var tph exprType = invalidType
	var rtpl exprType = invalidType
	var rtph exprType = invalidType

	tph, err = opTypeFromSpec(n.name, 3)
	if err != nil {
		return tph, tpl, rtpl, rtph, err
	}
	tpl, err = opTypeFromSpec(n.name, 2)
	if err != nil {
		return tph, tpl, rtpl, rtph, err
	}
	rtph, err = opTypeFromSpec(n.name, 1)
	if err != nil {
		return tph, tpl, rtpl, rtph, err
	}
	rtpl, err = opTypeFromSpec(n.name, 0)
	return tph, tpl, rtpl, rtph, err
}

func (n *funCallNode) checkBuiltinArgs() (argErrorPos int, err error) {
	args := n.children()
	for i, arg := range args {
		tp, err := argOpTypeFromSpec(n.name, i)
		if err != nil {
			return i, err
		}
		argExpr := arg.(ExprNodeIf)
		actualType, err := argExpr.getType()
		if err != nil {
			return i, err
		}
		if tp != unknownType && actualType != unknownType && actualType != tp {
			return i, fmt.Errorf("incompatible types: (exp) %s vs %s (actual) in expr '%s'", tp, actualType, n)
		}
	}
	return
}

func (n *funCallNode) resolveFieldArg(field string) (err error) {
	tp, err := runtimeFieldTypeFromSpec(n.name, field)
	if err != nil {
		return
	}
	n.field = field
	n.funType = tp
	return
}

func (n *runtimeFieldNode) getType() (exprType, error) {
	if n.exprType != unknownType {
		return n.exprType, nil
	}

	tp, err := runtimeFieldTypeFromSpec(n.op, n.field)
	if err != nil {
		return invalidType, fmt.Errorf("lookup failed: %s", err.Error())
	}

	n.exprType = tp
	return tp, err
}

func (n *runtimeArgNode) getType() (exprType, error) {
	if n.exprType != unknownType {
		return n.exprType, nil
	}

	tp, err := opTypeFromSpec(n.op, 0)
	if err != nil {
		return invalidType, fmt.Errorf("lookup failed: %s", err.Error())
	}

	n.exprType = tp
	return tp, err
}

func (n *constNode) getType() (exprType, error) {
	return n.exprType, nil
}

func (n *typeCastNode) getType() (exprType, error) {
	exprType, err := n.expr.getType()
	if err != nil {
		return unknownType, err
	}
	if exprType != unknownType && exprType != n.targetType {
		return unknownType, fmt.Errorf("cannot cast %s to %s", exprType.String(), n.targetType.String())
	}
	return n.targetType, nil
}

//--------------------------------------------------------------------------------------------------
//
// Common node methods
//
//--------------------------------------------------------------------------------------------------

func (n *TreeNode) append(ch TreeNodeIf) {
	n.childrenNodes = append(n.childrenNodes, ch)
}

func (n *TreeNode) children() []TreeNodeIf {
	return n.childrenNodes
}

func (n *TreeNode) String() string {
	return n.nodeName
}

func (n *TreeNode) parent() TreeNodeIf {
	return n.parentNode
}

// Print AST and context
func (n *TreeNode) Print() {
	printImpl(n, 0)

	n.ctx.Print()
}

func printImpl(n TreeNodeIf, offset int) {
	fmt.Printf("%s%s\n", strings.Repeat(" ", offset), n.String())
	for _, ch := range n.children() {
		printImpl(ch, offset+4)
	}
}

func (n *varDeclNode) String() string {
	return fmt.Sprintf("var (%s) %s = %s", n.exprType, n.name, n.value)
}

func (n *varDeclTupleNode) String() string {
	return fmt.Sprintf("var (%s) %s, %s = %s", n.exprType, n.high, n.low, n.value)
}

func (n *constNode) String() string {
	return fmt.Sprintf("const (%s) %s = %s", n.exprType, n.name, n.value)
}

func (n *funDefNode) String() string {
	return fmt.Sprintf("function %s", n.name)
}

func (n *exprIdentNode) String() string {
	return fmt.Sprintf("ident %s", n.name)
}

func (n *exprLiteralNode) String() string {
	return n.value
}

func (n *exprBinOpNode) String() string {
	return fmt.Sprintf("%s %s %s", n.lhs, n.op, n.rhs)
}

func (n *exprUnOpNode) String() string {
	return fmt.Sprintf("%s %s", n.op, n.value)
}

func (n *exprGroupNode) String() string {
	return fmt.Sprintf("(%s)", n.value)
}

func (n *ifExprNode) String() string {
	return fmt.Sprintf("if %s { %s } else { %s }", n.condExpr, n.condTrueExpr, n.condFalseExpr)
}

func (n *forStatementNode) String() string {
	return fmt.Sprintf("for %s { %s}", n.condExpr, n.condTrueExpr)
}

func (n *returnNode) String() string {
	return fmt.Sprintf("return %s", n.value)
}

func (n *assignNode) String() string {
	return fmt.Sprintf("%s = %s", n.name, n.value)
}

func (n *ifStatementNode) String() string {
	return fmt.Sprintf("if %s", n.condExpr)
}

func (n *funCallNode) String() string {
	return fmt.Sprintf("%s (%v)", n.name, n.children())
}

func (n *runtimeFieldNode) String() string {
	switch n.op {
	case "gtxn":
		return fmt.Sprintf("%s[%s].%s\n", n.op, n.index1, n.field)
	case "gtxna":
		return fmt.Sprintf("%s[%s].%s[%s]\n", n.op, n.index1, n.field, n.index2)
	case "txna":
		return fmt.Sprintf("%s.%s[%s]\n", n.op, n.field, n.index1)
	case "txnas":
		return fmt.Sprintf("%s.%s[var]\n", n.op, n.field)
	default:
		return fmt.Sprintf("%s.%s\n", n.op, n.field)
	}
}
