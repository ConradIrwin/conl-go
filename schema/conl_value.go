package schema

import (
	"fmt"

	"github.com/ConradIrwin/conl-go"
)

type entry struct {
	key       *conl.Token
	value     *conlValue
	parentLno int
}

type conlValue struct {
	Scalar *conl.Token
	Map    []entry
	List   []entry
}

func parseDoc(input []byte) *conlValue {
	root := &conlValue{}
	stack := []*conlValue{root}
	lnoStack := []int{0}
	noValue := &conlValue{}
	lastLno := 0

	for token := range conl.Tokens(input) {
		current := stack[len(stack)-1]
		parentLno := lnoStack[len(stack)-1]

		switch token.Kind {
		case conl.MapKey:
			lastLno = token.Lno
			current.Map = append(current.Map, entry{&token, noValue, parentLno})

		case conl.ListItem:
			lastLno = token.Lno
			current.List = append(current.List, entry{&token, noValue, parentLno})

		case conl.Scalar, conl.MultilineScalar:
			if len(current.Map) > 0 {
				current.Map[len(current.Map)-1].value = &conlValue{Scalar: &token}
			} else {
				current.List[len(current.List)-1].value = &conlValue{Scalar: &token}
			}

		case conl.Indent:
			if len(current.Map) > 0 {
				current.Map[len(current.Map)-1].value = &conlValue{}
				stack = append(stack, current.Map[len(current.Map)-1].value)
				lnoStack = append(lnoStack, lastLno)
			} else {
				current.List[len(current.List)-1].value = &conlValue{}
				stack = append(stack, current.List[len(current.List)-1].value)
				lnoStack = append(lnoStack, lastLno)
			}
		case conl.Outdent:
			stack = stack[:len(stack)-1]
			lnoStack = lnoStack[:len(lnoStack)-1]

		case conl.NoValue, conl.MultilineHint, conl.Comment:
		default:
			panic(fmt.Errorf("%v: missing case %#v", token.Lno, token))
		}
	}

	return root
}
