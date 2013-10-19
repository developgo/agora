package runtime

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"sort"

	"github.com/PuerkitoBio/agora/bytecode"
	"github.com/PuerkitoBio/gocoro"
)

type valStack struct {
	st []Val
	sp int
}

func newValStack(sz int64) valStack {
	return valStack{
		st: make([]Val, 0, sz),
	}
}

// Push a value onto the stack.
func (vs *valStack) push(v Val) {
	// Stack has to grow as needed, StackSz doesn't take into account the loops
	if vs.sp == len(vs.st) {
		vs.st = append(vs.st, v)
	} else {
		vs.st[vs.sp] = v
	}
	vs.sp++
}

// Pop a value from the stack.
func (vs *valStack) pop() Val {
	vs.sp--
	v := vs.st[vs.sp]
	vs.st[vs.sp] = Nil // free this reference for gc
	return v
}

type bkmStack struct {
	st []int
	sp int
}

func (bs *bkmStack) push(i int) {
	if bs.sp == len(bs.st) {
		bs.st = append(bs.st, i)
	} else {
		bs.st[bs.sp] = i
	}
	bs.sp++
}

func (bs *bkmStack) pop() int {
	bs.sp--
	return bs.st[bs.sp]
}

// An agoraFuncVM is a runnable instance of a function value. It holds the virtual machine
// required to execute the instructions.
type agoraFuncVM struct {
	// Func info
	val   *agoraFuncVal
	proto *agoraFuncDef
	debug bool

	// Stacks and counters
	pc  int // program counter
	stk valStack
	rng rangeStack
	bkm bkmStack

	// Variables
	vars map[string]Val
	this Val
	args Val
}

// Instantiate a runnable representation of the function prototype.
func newFuncVM(fv *agoraFuncVal) *agoraFuncVM {
	p := fv.proto
	return &agoraFuncVM{
		val:   fv,
		proto: p,
		debug: p.ctx.Debug,
		stk:   newValStack(p.stackSz),
		vars:  make(map[string]Val, len(p.lTable)),
	}
}

// Get a value from *somewhere*, depending on the flag.
func (f *agoraFuncVM) getVal(flg bytecode.Flag, ix uint64) Val {
	switch flg {
	case bytecode.FLG_K:
		return f.proto.kTable[ix]
	case bytecode.FLG_V:
		// Fail if variable cannot be found
		varNm := f.proto.kTable[ix].String()
		v, ok := f.proto.ctx.getVar(varNm, f)
		if !ok {
			panic("variable not found: " + varNm)
		}
		return v
	case bytecode.FLG_N:
		return Nil
	case bytecode.FLG_T:
		return f.this
	case bytecode.FLG_F:
		return newAgoraFuncVal(f.proto.mod.fns[ix], f)
	case bytecode.FLG_A:
		return f.args
	}
	panic(fmt.Sprintf("invalid flag value %d", flg))
}

// Pretty-print an instruction.
func (f *agoraFuncVM) dumpInstrInfo(w io.Writer, i bytecode.Instr) {
	switch i.Flag() {
	case bytecode.FLG_K:
		v := f.proto.kTable[i.Index()]
		fmt.Fprintf(w, " ; %s", dumpVal(v))
	case bytecode.FLG_V:
		fmt.Fprintf(w, " ; var %s", f.proto.kTable[i.Index()])
	case bytecode.FLG_N:
		fmt.Fprintf(w, " ; %s", Nil.Dump())
	case bytecode.FLG_T:
		fmt.Fprint(w, " ; [this]")
	case bytecode.FLG_F:
		fmt.Fprintf(w, " ; [func %s]", f.proto.mod.fns[i.Index()].name)
	case bytecode.FLG_A:
		fmt.Fprint(w, " ; [args]")
	}
}

// Pretty-print a function's execution context.
func (f *agoraFuncVM) dump() string {
	buf := bytes.NewBuffer(nil)
	fmt.Fprintf(buf, "\n> %s\n", f.val.Dump())
	// Constants
	fmt.Fprintf(buf, "  Constants:\n")
	for i, v := range f.proto.kTable {
		fmt.Fprintf(buf, "    [%3d] %s\n", i, dumpVal(v))
	}
	// Variables
	fmt.Fprintf(buf, "\n  Variables:\n")
	if f.this != nil {
		fmt.Fprintf(buf, "    [this] = %s\n", dumpVal(f.this))
	}
	if f.args != nil {
		fmt.Fprintf(buf, "    [args] = %s\n", dumpVal(f.args))
	}
	// Sort the vars for deterministic output
	sortedVars := make([]string, len(f.vars))
	j := 0
	for k, _ := range f.vars {
		sortedVars[j] = k
		j++
	}
	sort.Strings(sortedVars)
	for _, k := range sortedVars {
		fmt.Fprintf(buf, "    %s = %s\n", k, dumpVal(f.vars[k]))
	}
	// Stack
	fmt.Fprintf(buf, "\n  Stack:\n")
	i := int(math.Max(0, float64(f.stk.sp-5)))
	for i <= f.stk.sp {
		if i == f.stk.sp {
			fmt.Fprint(buf, "sp->")
		} else {
			fmt.Fprint(buf, "    ")
		}
		v := Val(Nil)
		if i < len(f.stk.st) {
			v = f.stk.st[i]
		}
		fmt.Fprintf(buf, "[%3d] %s\n", i, dumpVal(v))
		i++
	}
	// Instructions
	fmt.Fprintf(buf, "\n  Instructions:\n")
	i = int(math.Max(0, float64(f.pc-10)))
	for i <= f.pc+10 {
		if i == f.pc {
			fmt.Fprintf(buf, "pc->")
		} else {
			fmt.Fprintf(buf, "    ")
		}
		if i < len(f.proto.code) {
			fmt.Fprintf(buf, "[%3d] %s", i, f.proto.code[i])
			f.dumpInstrInfo(buf, f.proto.code[i])
			fmt.Fprintln(buf)
		} else {
			break
		}
		i++
	}
	fmt.Fprintln(buf)
	return buf.String()
}

// Create the reserved identifier `args` value, as an Object.
func (vm *agoraFuncVM) createArgsVal(args []Val) Val {
	if len(args) == 0 {
		return Nil
	}
	o := NewObject()
	for i, v := range args {
		o.Set(Number(i), v)
	}
	return o
}

// Create the local variables all initialized to nil
func (vm *agoraFuncVM) createLocals() {
	for _, s := range vm.proto.lTable {
		vm.vars[s] = Nil
	}
}

// run executes the instructions of the function. This is the actual implementation
// of the Virtual Machine.
func (f *agoraFuncVM) run(args ...Val) []Val {
	// Register the defer to release all `for range` coroutines created
	// by the VM and possibly still alive from a resume of this VM.
	clearRange := true
	defer func() {
		if clearRange {
			f.rng.clear()
		}
	}()

	// Keep reference to arithmetic and comparer
	arith := f.proto.ctx.Arithmetic
	cmp := f.proto.ctx.Comparer

	// If the program counter is 0, this is an initial run, not a resume as
	// a coroutine.
	if f.pc == 0 {
		// Create local variables
		f.createLocals()

		// Expected args are defined in constant table spots 0 to ExpArgs - 1.
		for j, l := int64(0), int64(len(args)); j < f.proto.expArgs; j++ {
			if j < l {
				f.vars[f.proto.kTable[j].String()] = args[j]
			} else {
				f.vars[f.proto.kTable[j].String()] = Nil
			}
		}
		// Keep the args array
		f.args = f.createArgsVal(args)
	} else {
		// This is a resume for a coroutine, push the received arg (only one) on the stack
		var a0 Val = Nil
		if len(args) > 0 {
			a0 = args[0]
		}
		f.stk.push(a0)
	}

	// Execute the instructions
	for {
		// Get the instruction to process
		i := f.proto.code[f.pc]
		// Decode the instruction
		op, flg, ix := i.Opcode(), i.Flag(), i.Index()
		// Increment the PC, if a jump requires a different PC delta, it will set it explicitly
		f.pc++
		switch op {
		case bytecode.OP_RET:
			// End this function call, return the value on top of the stack and remove
			// the vm if it was set on the value
			f.val.coroState = nil
			return Set1(f.stk.pop())

		case bytecode.OP_YLD:
			// Yield n value(s), save the vm so it can be called back, and return
			f.val.coroState = f
			clearRange = false // Keep active range coros, so that they can continue on a resume
			return Set1(f.stk.pop())

		case bytecode.OP_PUSH:
			f.stk.push(f.getVal(flg, ix))

		case bytecode.OP_POP:
			if nm, v := f.proto.kTable[ix].String(), f.stk.pop(); !f.proto.ctx.setVar(nm, v, f) {
				// Not found anywhere, panic
				panic("unknown variable: " + nm)
			}

		case bytecode.OP_ADD:
			y, x := f.stk.pop(), f.stk.pop()
			f.stk.push(arith.Add(x, y))

		case bytecode.OP_SUB:
			y, x := f.stk.pop(), f.stk.pop()
			f.stk.push(arith.Sub(x, y))

		case bytecode.OP_MUL:
			y, x := f.stk.pop(), f.stk.pop()
			f.stk.push(arith.Mul(x, y))

		case bytecode.OP_DIV:
			y, x := f.stk.pop(), f.stk.pop()
			f.stk.push(arith.Div(x, y))

		case bytecode.OP_MOD:
			y, x := f.stk.pop(), f.stk.pop()
			f.stk.push(arith.Mod(x, y))

		case bytecode.OP_NOT:
			x := f.stk.pop()
			f.stk.push(Bool(!x.Bool()))

		case bytecode.OP_UNM:
			x := f.stk.pop()
			f.stk.push(arith.Unm(x))

		case bytecode.OP_EQ:
			y, x := f.stk.pop(), f.stk.pop()
			f.stk.push(Bool(cmp.Cmp(x, y) == 0))

		case bytecode.OP_NEQ:
			y, x := f.stk.pop(), f.stk.pop()
			f.stk.push(Bool(cmp.Cmp(x, y) != 0))

		case bytecode.OP_LT:
			y, x := f.stk.pop(), f.stk.pop()
			f.stk.push(Bool(cmp.Cmp(x, y) < 0))

		case bytecode.OP_LTE:
			y, x := f.stk.pop(), f.stk.pop()
			f.stk.push(Bool(cmp.Cmp(x, y) <= 0))

		case bytecode.OP_GT:
			y, x := f.stk.pop(), f.stk.pop()
			f.stk.push(Bool(cmp.Cmp(x, y) > 0))

		case bytecode.OP_GTE:
			y, x := f.stk.pop(), f.stk.pop()
			f.stk.push(Bool(cmp.Cmp(x, y) >= 0))

		case bytecode.OP_TEST:
			if !f.stk.pop().Bool() {
				// Do the jump over ix instructions
				f.pc += int(ix)
			}

		case bytecode.OP_JMP:
			if flg == bytecode.FLG_Jf {
				f.pc += int(ix)
			} else {
				f.pc -= (int(ix) + 1) // +1 because pc is already on next instr
			}

		case bytecode.OP_NEW:
			ob := NewObject()
			for j := ix; j > 0; j-- {
				key, val := f.stk.pop(), f.stk.pop()
				ob.Set(key, val)
			}
			f.stk.push(ob)

		case bytecode.OP_SFLD:
			vr, k, vl := f.stk.pop(), f.stk.pop(), f.stk.pop()
			if ob, ok := vr.(Object); ok {
				ob.Set(k, vl)
			} else {
				panic(NewTypeError(Type(vr), "", "object"))
			}

		case bytecode.OP_GFLD:
			vr, k := f.stk.pop(), f.stk.pop()
			if ob, ok := vr.(Object); ok {
				f.stk.push(ob.Get(k))
			} else {
				panic(NewTypeError(Type(vr), "", "object"))
			}

		case bytecode.OP_CFLD:
			vr, k := f.stk.pop(), f.stk.pop()
			// Pop the arguments in reverse order
			args := make([]Val, ix)
			for j := ix; j > 0; j-- {
				args[j-1] = f.stk.pop()
			}
			if ob, ok := vr.(Object); ok {
				vals := ob.callMethod(k, args...)
				for _, v := range vals {
					f.stk.push(v)
				}
			} else {
				panic(NewTypeError(Type(vr), "", "object"))
			}

		case bytecode.OP_CALL:
			// ix is the number of args
			// Pop the function itself, ensure it is a function
			x := f.stk.pop()
			fn, ok := x.(Func)
			if !ok {
				panic(NewTypeError(Type(x), "", "func"))
			}
			// Pop the arguments in reverse order
			args := make([]Val, ix)
			for j := ix; j > 0; j-- {
				args[j-1] = f.stk.pop()
			}
			// Call the function, and store the return value(s) on the stack
			vals := fn.Call(nil, args...)
			for _, v := range vals {
				f.stk.push(v)
			}

		case bytecode.OP_RNGS:
			// Pop the arguments in reverse order
			args := make([]Val, ix)
			for j := ix; j > 0; j-- {
				args[j-1] = f.stk.pop()
			}
			// Create the range coroutine
			f.rng.push(args...)

		case bytecode.OP_RNGP:
			coro := f.rng.st[f.rng.sp-1]
			v, e := coro.Resume()
			var vals []interface{}
			if sl, ok := v.([]interface{}); ok {
				vals = sl
			} else {
				vals = []interface{}{v}
			}
			// Push the values
			if e == nil {
				for j := uint64(0); j < ix; j++ {
					if j < uint64(len(vals)) {
						f.stk.push(vals[j].(Val))
					} else {
						f.stk.push(Nil)
					}
				}
			} else if e != gocoro.ErrEndOfCoro {
				panic(e)
			}
			// Push the condition
			f.stk.push(Bool(e == nil))

		case bytecode.OP_RNGE:
			// Release the range coroutine
			f.rng.pop()

		case bytecode.OP_BKMS:
			// Push the current stack index on the bookmark stack
			f.bkm.push(f.stk.sp)

		case bytecode.OP_BKME:
			// Leave exactly ix values on the stack, from the last bookmark
			bkm := f.bkm.pop()
			for got := uint64(f.stk.sp - bkm); got != ix; got = uint64(f.stk.sp - bkm) {
				if got < ix {
					f.stk.push(Nil)
				} else {
					f.stk.pop()
				}
			}

		case bytecode.OP_DUMP:
			if f.debug {
				// Dumps `ix` number of stack traces
				f.proto.ctx.dump(int(ix))
			}

		default:
			panic(fmt.Sprintf("unknown opcode %s", op))
		}
	}
}
