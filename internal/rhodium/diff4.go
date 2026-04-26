package rhodium

// diff4 implements the M1 (fast-path) classifier for Iron-style 4-way diffs.
//
// The diamond has four corners:
//
//	    f2  (new tip — current PR head)
//	   /  \
//	  f1    b2   (old tip | new base)
//	   \  /
//	    b1  (old base)
//
// The classifier determines one of 15 equivalence classes based on which
// corners have equal content. Each class maps to a display strategy:
//   - Hidden: nothing to show (clean rebase, forget, no change)
//   - ShownAsDiff2: display as a simple 2-way diff between two corners
//   - Complex: needs full segmentation (M2+); for now, fall back to a 2-way diff

// Corner identifies one of the four diamond points.
type Corner int

const (
	B1 Corner = iota // old base
	F1               // old tip (what the reviewer last saw)
	B2               // new base (after rebase; equals B1 if no rebase)
	F2               // new tip (current PR head)
)

func (c Corner) String() string {
	switch c {
	case B1:
		return "b1"
	case F1:
		return "f1"
	case B2:
		return "b2"
	case F2:
		return "f2"
	default:
		return "?"
	}
}

// Diamond holds content for all four corners.
type Diamond struct {
	B1, F1, B2, F2 string
}

// Get returns the content at the given corner.
func (d Diamond) Get(c Corner) string {
	switch c {
	case B1:
		return d.B1
	case F1:
		return d.F1
	case B2:
		return d.B2
	case F2:
		return d.F2
	default:
		return ""
	}
}

// Class is one of 15 diamond equivalence classes. Named by which corners
// share equal content. Double-underscore separates independent equality groups.
type Class int

const (
	// Hidden classes — nothing to show the reviewer.
	ClassB1B2F1F2 Class = iota // all equal
	ClassB1B2__F1F2            // bases equal, features equal (clean merge)
	ClassB1F1__B2F2            // old pair equal, new pair equal (clean merge)
	ClassB2F1F2                // FORGET: base absorbed feature changes

	// Shown-as-diff2 classes — display as a simple 2-way diff.
	ClassB1B2F1    // b1=b2=f1; only f2 is new → show b2→f2
	ClassB1B2F2    // b1=b2=f2; f1 is the outlier → show b1→f1
	ClassB1B2      // bases equal, features differ → show f1→f2 (most common: new commits, no rebase)
	ClassB1F1F2    // b1=f1=f2; only b2 changed → show b1→b2
	ClassB2F1      // b2=f1 → show b2→f2
	ClassB1F2__B2F1 // cross-equal → show b2→f2

	// Complex classes — need segmentation (M2+); M1 falls back to 2-way.
	ClassB1F1    // old pair equal
	ClassB1F2    // diagonal equal
	ClassB2F2    // new pair equal
	ClassF1F2    // features equal
	ClassConflict // all four different
)

func (c Class) String() string {
	switch c {
	case ClassB1B2F1F2:
		return "b1_b2_f1_f2"
	case ClassB1B2__F1F2:
		return "b1_b2__f1_f2"
	case ClassB1F1__B2F2:
		return "b1_f1__b2_f2"
	case ClassB2F1F2:
		return "b2_f1_f2"
	case ClassB1B2F1:
		return "b1_b2_f1"
	case ClassB1B2F2:
		return "b1_b2_f2"
	case ClassB1B2:
		return "b1_b2"
	case ClassB1F1F2:
		return "b1_f1_f2"
	case ClassB2F1:
		return "b2_f1"
	case ClassB1F2__B2F1:
		return "b1_f2__b2_f1"
	case ClassB1F1:
		return "b1_f1"
	case ClassB1F2:
		return "b1_f2"
	case ClassB2F2:
		return "b2_f2"
	case ClassF1F2:
		return "f1_f2"
	case ClassConflict:
		return "conflict"
	default:
		return "unknown"
	}
}

// Hidden returns true if the class requires no display to the reviewer.
func (c Class) Hidden() bool {
	switch c {
	case ClassB1B2F1F2, ClassB1B2__F1F2, ClassB1F1__B2F2, ClassB2F1F2:
		return true
	default:
		return false
	}
}

// ShownAsDiff2 returns true if the class can be rendered as a simple 2-way diff.
func (c Class) ShownAsDiff2() bool {
	switch c {
	case ClassB1B2F1, ClassB1B2F2, ClassB1B2, ClassB1F1F2, ClassB2F1, ClassB1F2__B2F1:
		return true
	default:
		return false
	}
}

// IsForget returns true for the FORGET class (b2=f1=f2): base absorbed feature.
func (c Class) IsForget() bool {
	return c == ClassB2F1F2
}

// View describes how to display a segment: diff the content at From against To.
type View struct {
	From Corner
	To   Corner
	Kind string // human-readable label
}

// Views returns the display views for this class. Hidden classes return nil.
// Shown-as-diff2 classes return exactly one view. Complex classes return
// multiple views (the primary one first).
func (c Class) Views() []View {
	switch c {
	// Hidden — nothing.
	case ClassB1B2F1F2, ClassB1B2__F1F2, ClassB1F1__B2F2, ClassB2F1F2:
		return nil

	// Shown as diff2.
	case ClassB1B2F1:
		return []View{{B2, F2, "new diff"}}
	case ClassB1B2F2:
		return []View{{F1, F2, "dropped feature change"}}
	case ClassB1B2:
		return []View{{F1, F2, "diff extension"}}
	case ClassB1F1F2:
		return []View{{B2, F2, "dropped base change"}}
	case ClassB2F1:
		return []View{{B2, F2, "diff extension"}}
	case ClassB1F2__B2F1:
		return []View{{B2, F2, "dropped same change"}}

	// Complex — primary view first, others for richer display (M5+).
	case ClassB1F1:
		return []View{{B2, F2, "new diff"}}
	case ClassB1F2:
		return []View{{B2, F2, "new diff"}, {F1, F2, "old tip to new tip"}}
	case ClassB2F2:
		return []View{{B1, F1, "old diff"}, {F1, F2, "old tip to new tip"}}
	case ClassF1F2:
		return []View{{B2, F2, "new diff"}, {B1, B2, "base change"}}
	case ClassConflict:
		return []View{{B2, F2, "new diff"}, {F1, F2, "old tip to new tip"}, {B1, B2, "base change"}}
	default:
		return nil
	}
}

// Classify determines the equivalence class of a diamond by comparing all
// pairs of corners for content equality.
func Classify(d Diamond, eq func(a, b string) bool) Class {
	if eq == nil {
		eq = func(a, b string) bool { return a == b }
	}

	b1b2 := eq(d.B1, d.B2)
	b1f1 := eq(d.B1, d.F1)
	b1f2 := eq(d.B1, d.F2)
	b2f1 := eq(d.B2, d.F1)
	b2f2 := eq(d.B2, d.F2)
	f1f2 := eq(d.F1, d.F2)

	// Classification by partition. Check from most-equal to least-equal.
	// All four equal.
	if b1b2 && b1f1 && b1f2 {
		return ClassB1B2F1F2
	}

	// Three equal, one outlier (4 cases).
	if b1b2 && b1f1 { // b1=b2=f1, f2 differs
		return ClassB1B2F1
	}
	if b1b2 && b1f2 { // b1=b2=f2, f1 differs
		return ClassB1B2F2
	}
	if b1f1 && f1f2 { // b1=f1=f2, b2 differs
		return ClassB1F1F2
	}
	if b2f1 && f1f2 { // b2=f1=f2, b1 differs → FORGET
		return ClassB2F1F2
	}

	// Two pairs equal (3 cases: 2+2 partitions).
	if b1b2 && f1f2 {
		return ClassB1B2__F1F2
	}
	if b1f1 && b2f2 {
		return ClassB1F1__B2F2
	}
	if b1f2 && b2f1 {
		return ClassB1F2__B2F1
	}

	// One pair equal (6 cases: 2+1+1 partitions).
	if b1b2 {
		return ClassB1B2
	}
	if b2f1 {
		return ClassB2F1
	}
	if b1f1 {
		return ClassB1F1
	}
	if b1f2 {
		return ClassB1F2
	}
	if b2f2 {
		return ClassB2F2
	}
	if f1f2 {
		return ClassF1F2
	}

	// All different.
	return ClassConflict
}

// Segment represents a classified region of the diamond. For M1 (fast path),
// a result contains at most one segment covering the whole file.
type Segment struct {
	Class Class
	B1    string
	F1    string
	B2    string
	F2    string
}

// Result is the output of Compute: a list of classified segments.
type Result struct {
	Segments []Segment
}

// Compute classifies the diamond and returns a result. M1 implementation:
//   - Hidden class → empty result (nothing to show)
//   - Shown-as-diff2 class → single whole-file segment
//   - Complex class → single whole-file segment (caller falls back to 2-way diff)
func Compute(d Diamond) *Result {
	class := Classify(d, nil)

	if class.Hidden() {
		return &Result{}
	}

	return &Result{
		Segments: []Segment{{
			Class: class,
			B1:    d.B1,
			F1:    d.F1,
			B2:    d.B2,
			F2:    d.F2,
		}},
	}
}
