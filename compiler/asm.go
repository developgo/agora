package compiler

import (
	"bufio"
	"io"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goblin/runtime"
)

var (
	m map[string]func(*runtime.GoblinFunc)
)

func Compile(id string, r io.Reader) (runtime.Module, error) {
	s := bufio.NewScanner(r)
	mod := runtime.NewGoblinModule(id)

	m = map[string]func(*runtime.GoblinFunc){
		"[f]": func(_ *runtime.GoblinFunc) {
			var p *runtime.GoblinFunc
			i := 0
			for s.Scan() {
				switch i {
				case 0:
					if s.Text() == "true" {
						p.IsNative = true
						i = 2
					} else {
						// Stack size
						p.StackSz, _ = strconv.Atoi(s.Text())
					}
				case 1:
					// Expected args count
					p.ExpArgs, _ = strconv.Atoi(s.Text())
				case 2:
					// Expected vars count
					p.ExpVars, _ = strconv.Atoi(s.Text())
				case 3:
					p.Name = s.Text()
					if p.IsNative {
						i = 1000
					}
				case 4:
					// File name
					p.Dbg.File = s.Text()
				case 5:
					// Line start
					p.Dbg.LineStart, _ = strconv.Atoi(s.Text())
				case 6:
					// Line end
					p.Dbg.LineEnd, _ = strconv.Atoi(s.Text())
				default:
					ctx.Protos = append(ctx.Protos, p)
					// Find where to go from here
					f := m[s.Text()]
					f(p)
				}
				i++
			}
			// If finished scanning, but last p is native func, then it hasn't been added
			// to the Protos, because there's no other section in a native func (no [v] or [k]...)
			if p.IsNative {
				ctx.Protos = append(ctx.Protos, p)
			}
		},

		"[k]": func(p *runtime.FuncProto) {
			for s.Scan() {
				tline := strings.TrimSpace(s.Text())
				if f, ok := m[tline]; ok {
					f(p)
					return
				}
				line := s.Text()

				switch line[0] {
				case 'i':
					// Integer
					i := runtime.String(line[1:]).Int()
					p.KTable = append(p.KTable, runtime.Int(i))
				case 'f':
					// Float
					f := runtime.String(line[1:]).Float()
					p.KTable = append(p.KTable, runtime.Float(f))
				case 's':
					// String
					p.KTable = append(p.KTable, runtime.String(line[1:]))
				case 'b':
					// Boolean
					p.KTable = append(p.KTable, runtime.Bool(line[1] == '1'))
				case 'n':
					// Nil
					p.KTable = append(p.KTable, runtime.Nil)
				default:
					panic("invalid constant value type")
				}
			}
			panic("missing instructions section [i]")
		},

		"[i]": func(p *runtime.FuncProto) {
			for s.Scan() {
				line := strings.TrimSpace(s.Text())
				if f, ok := m[line]; ok {
					f(p)
					return
				}
				parts := strings.Fields(line)
				l := len(parts)
				var (
					op  runtime.Opcode
					flg runtime.Flag
					ix  int64
				)
				op = runtime.NewOpcode(parts[0])
				if l > 1 {
					flg = runtime.NewFlag(parts[1])
					ix, _ = strconv.ParseInt(parts[2], 10, 64)
				}
				p.Code = append(p.Code, runtime.NewInstr(op, flg, uint64(ix)))
			}
		},
	}

	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if f, ok := m[line]; ok {
			f(nil)
		}
	}
	return ctx
}
