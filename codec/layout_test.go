package codec

import (
	"errors"
	"testing"
)

func TestNewRegisterLayout_Valid(t *testing.T) {
	l, err := NewRegisterLayout(2, 4, 3, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if l.RegisterCount() != 2 {
		t.Errorf("RegisterCount() = %d, want 2", l.RegisterCount())
	}
	pos := l.BytePositions()
	if len(pos) != 4 {
		t.Fatalf("BytePositions() length = %d, want 4", len(pos))
	}
	if pos[0] != 4 || pos[1] != 3 || pos[2] != 2 || pos[3] != 1 {
		t.Errorf("BytePositions() = %v, want [4 3 2 1]", pos)
	}
	if s := l.String(); s != "4321" {
		t.Errorf("String() = %q, want \"4321\"", s)
	}
}

func TestNewRegisterLayout_InvalidCount(t *testing.T) {
	_, err := NewRegisterLayout(0, 1, 2)
	if err == nil {
		t.Fatal("expected error for register count 0")
	}
	if !errors.Is(err, ErrInvalidLayout) {
		t.Errorf("expected ErrInvalidLayout, got %v", err)
	}

	_, err = NewRegisterLayout(5, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	if err == nil {
		t.Fatal("expected error for register count 5")
	}
}

func TestNewRegisterLayout_WrongLength(t *testing.T) {
	_, err := NewRegisterLayout(2, 4, 3)
	if err == nil {
		t.Fatal("expected error for 3 positions when 4 required")
	}
	if !errors.Is(err, ErrInvalidLayout) {
		t.Errorf("expected ErrInvalidLayout, got %v", err)
	}
}

func TestNewRegisterLayout_DuplicatePosition(t *testing.T) {
	_, err := NewRegisterLayout(2, 4, 3, 4, 1)
	if err == nil {
		t.Fatal("expected error for duplicate position 4")
	}
	if !errors.Is(err, ErrInvalidLayout) {
		t.Errorf("expected ErrInvalidLayout, got %v", err)
	}
}

func TestNewRegisterLayout_OutOfRange(t *testing.T) {
	_, err := NewRegisterLayout(2, 4, 3, 2, 9)
	if err == nil {
		t.Fatal("expected error for position 9 (max 4)")
	}
	_, err = NewRegisterLayout(2, 0, 3, 2, 1)
	if err == nil {
		t.Fatal("expected error for position 0 (out of range)")
	}
}

func TestMustNewRegisterLayout_Panic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic")
		}
	}()
	MustNewRegisterLayout(2, 4, 3)
}

func TestNamedLayouts_String(t *testing.T) {
	tests := []struct {
		name string
		l    RegisterLayout
		want string
	}{
		{"Layout16_21", Layout16_21, "21"},
		{"Layout16_12", Layout16_12, "12"},
		{"Layout32_4321", Layout32_4321, "4321"},
		{"Layout32_3412", Layout32_3412, "3412"},
		{"Layout32_2143", Layout32_2143, "2143"},
		{"Layout32_1234", Layout32_1234, "1234"},
		{"Layout48_654321", Layout48_654321, "654321"},
		{"Layout48_563412", Layout48_563412, "563412"},
		{"Layout48_214365", Layout48_214365, "214365"},
		{"Layout48_123456", Layout48_123456, "123456"},
		{"Layout64_87654321", Layout64_87654321, "87654321"},
		{"Layout64_78563412", Layout64_78563412, "78563412"},
		{"Layout64_21436587", Layout64_21436587, "21436587"},
		{"Layout64_12345678", Layout64_12345678, "12345678"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.l.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRegisterLayout_BytePositionsCopy(t *testing.T) {
	l := Layout32_4321
	p := l.BytePositions()
	p[0] = 99
	p2 := l.BytePositions()
	if p2[0] != 4 {
		t.Errorf("BytePositions() should return copy; mutating first slice changed layout: got %d", p2[0])
	}
}
