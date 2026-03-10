package process

import (
	"testing"
)

type processorCfg struct {
	called bool
}

func (p *processorCfg) Process() {
	p.called = true
}

func TestProcess_CallsProcessor(t *testing.T) {
	c := &processorCfg{}
	Process(c)
	if !c.called {
		t.Error("expected Process() to be called")
	}
}

func TestProcess_NestedProcessor(t *testing.T) {
	type cfg struct {
		Inner processorCfg
	}

	c := &cfg{}
	Process(c)
	if !c.Inner.called {
		t.Error("expected nested Process() to be called")
	}
}

func TestProcess_DepthFirstOrder(t *testing.T) {
	var order []string

	type inner struct {
		orderedProcessorCfg
	}
	type cfg struct {
		Inner inner
		orderedProcessorCfg
	}

	c := &cfg{}
	c.orderedProcessorCfg.order = &order
	c.orderedProcessorCfg.name = "parent"
	c.Inner.orderedProcessorCfg.order = &order
	c.Inner.orderedProcessorCfg.name = "child"

	Process(c)

	if len(order) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(order))
	}
	if order[0] != "child" {
		t.Errorf("expected child first, got %q", order[0])
	}
	if order[1] != "parent" {
		t.Errorf("expected parent second, got %q", order[1])
	}
}

type orderedProcessorCfg struct {
	order *[]string
	name  string
}

func (o *orderedProcessorCfg) Process() {
	*o.order = append(*o.order, o.name)
}

func TestProcess_NoProcessor(t *testing.T) {
	type cfg struct {
		Name string
	}

	c := &cfg{Name: "test"}
	Process(c)
}

func TestProcess_PanicOnNonPointer(t *testing.T) {
	type cfg struct{}

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when passing non-pointer")
		}
	}()

	var c cfg
	Process(c)
}

func TestProcess_NestedPointerToProcessor(t *testing.T) {
	type cfg struct {
		Inner *processorCfg
	}

	c := &cfg{Inner: &processorCfg{}}
	Process(c)
	if !c.Inner.called {
		t.Error("expected nested pointer Process() to be called")
	}
}