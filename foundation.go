package uik

import (
	"image"
	"image/draw"
	"github.com/skelterjohn/go.wde"
	"github.com/skelterjohn/geom"
)

type CompositeBlockRequest struct {
	CompositeRequest
	Block *Block
}

// The foundation type is for channeling events to children, and passing along
// draw calls.
type Foundation struct {
	Block
	
	Children    []*Block
	ChildrenBounds map[*Block]geom.Rect
	ChildrenLastBuffers map[*Block]image.Image

	CompositeBlockRequests chan CompositeBlockRequest

	DragOriginBlocks map[wde.Button][]*Block

	// this block currently has keyboard priority
	KeyboardBlock    *Block
}

func (f *Foundation) Initialize() {
	f.Block.Initialize()
	f.CompositeBlockRequests = make(chan CompositeBlockRequest)
	f.ChildrenBounds = map[*Block]geom.Rect{}
	f.ChildrenLastBuffers = map[*Block]image.Image{}
	f.DragOriginBlocks = map[wde.Button][]*Block{}
}

func (f *Foundation) RemoveBlock(b *Block) {
	if b.Parent != f {
		// TODO: log
		return
	}
	close(b.Compositor)
	b.Compositor = nil
	if bounds, ok := f.ChildrenBounds[b]; ok {
		RedrawEventChan(f.Redraw).Stack(RedrawEvent{
			bounds,
		})
	}
	delete(f.ChildrenBounds, b)
	b.Parent = nil
}

func (f *Foundation) PlaceBlock(b *Block, bounds geom.Rect) {
	if b.Parent == nil {
		f.Children = append(f.Children, b)
		b.Parent = f
	} else if b.Parent != f {
		b.Parent.RemoveBlock(b)
		b.Parent = f
	}
	f.ChildrenBounds[b] = bounds

	b.Compositor = make(chan CompositeRequest)
	go func(b *Block, blockCompositor chan CompositeRequest) {
		for cr := range blockCompositor {
			f.CompositeBlockRequests <- CompositeBlockRequest {
				CompositeRequest: cr,
				Block: b,
			}
		}
	}(b, b.Compositor)
	RedrawEventChan(f.Redraw).Stack(RedrawEvent{
		bounds,
	})
}

func (f *Foundation) BlocksForCoord(p geom.Coord) (bs []*Block) {
	// quad-tree one day?
	for _, bl := range f.Children {
		bbs, ok := f.ChildrenBounds[bl]
		if !ok {
			continue
		}
		if bbs.ContainsCoord(p) {
			bs = append(bs, bl)
		}
	}
	return
}

func (f *Foundation) InvokeOnBlocksUnder(p geom.Coord, foo func(*Block)) {
	// quad-tree one day?
	for _, bl := range f.Children {
		bbs, ok := f.ChildrenBounds[bl]
		if !ok {
			continue
		}
		if bbs.ContainsCoord(p) {
			foo(bl)
			return
		}
	}
	return

}

func (f *Foundation) CompositeBlockBuffer(b *Block, buf image.Image) (composited bool) {
	bounds, ok := f.ChildrenBounds[b]
	if !ok {
		composited = false
		return
	}
	f.PrepareBuffer()
	draw.Draw(f.Buffer, RectangleForRect(bounds), buf, image.Point{0, 0}, draw.Over)
	composited = true
	return
}

func (f *Foundation) DoCompositeBlockRequest(cbr CompositeBlockRequest) {
	b := cbr.Block
	f.ChildrenLastBuffers[b] = cbr.Buffer
	f.CompositeBlockBuffer(b, cbr.Buffer)
	if f.Compositor != nil {
		f.Compositor <- CompositeRequest{
			Buffer: f.Buffer,
		}
	}
}

func (f *Foundation) DoRedraw(e RedrawEvent) {
	bgc := f.PrepareBuffer()
	f.DoPaint(bgc)
	for _, child := range f.Children {
		translatedDirty := e.Bounds
		bbs, ok := f.ChildrenBounds[child]
		if !ok { continue }

		translatedDirty.Min.X -= bbs.Min.X
		translatedDirty.Min.Y -= bbs.Min.Y

		RedrawEventChan(child.Redraw).Stack(RedrawEvent{translatedDirty})

		if buf, ok := f.ChildrenLastBuffers[child]; ok {
			f.CompositeBlockBuffer(child, buf)
		}
	}
	if f.Compositor != nil {
		f.Compositor <- CompositeRequest{
			Buffer: f.Buffer,
		}
	}
}

func (f *Foundation) DoMouseDownEvent(e MouseDownEvent) {
	f.InvokeOnBlocksUnder(e.Loc, func(b *Block) {
		bbs := f.ChildrenBounds[b]
		if b == nil {
			return
		}
		f.DragOriginBlocks[e.Which] = append(f.DragOriginBlocks[e.Which], b)
		e.Loc.X -= bbs.Min.X
		e.Loc.Y -= bbs.Min.Y
		b.allEventsIn <- e
	})
}

func (f *Foundation) DoMouseUpEvent(e MouseUpEvent) {
	touched := map[*Block]bool{}
	f.InvokeOnBlocksUnder(e.Loc, func(b *Block) {
		touched[b] = true
		bbs := f.ChildrenBounds[b]
		if b != nil {
			be := e
			be.Loc.X -= bbs.Min.X
			be.Loc.Y -= bbs.Min.Y
			b.allEventsIn <- be
		}
	})
	if origins, ok := f.DragOriginBlocks[e.Which]; ok {
		for _, origin := range origins {
			if touched[origin] {
				continue
			}
			oe := e
			obbs := f.ChildrenBounds[origin]
			oe.Loc.X -= obbs.Min.X
			oe.Loc.Y -= obbs.Min.Y
			origin.allEventsIn <- oe
		}
	}
	delete(f.DragOriginBlocks, e.Which)
}

func (f *Foundation) DoResizeEvent(e ResizeEvent) {
	if e.Size == f.Size {
		return
	}
	f.Size = e.Size
	f.Buffer = nil
}

// dispense events to children, as appropriate
func (f *Foundation) HandleEvents() {
	f.ListenedChannels[f.CloseEvents] = true
	f.ListenedChannels[f.MouseDownEvents] = true
	f.ListenedChannels[f.MouseUpEvents] = true
	// drag and up events for the same button get sent to the origin as well

	for {
		select {
		case e := <-f.CloseEvents:
			for _, b := range f.Children {
				b.allEventsIn <- e
			}
		case e := <-f.MouseDownEvents:
			f.DoMouseDownEvent(e)
		case e := <-f.MouseUpEvents:
			f.DoMouseUpEvent(e)
		case e := <-f.ResizeEvents:
			f.DoResizeEvent(e)
		case e := <-f.Redraw:
			f.DoRedraw(e)
		case cbr := <-f.CompositeBlockRequests:
			f.DoCompositeBlockRequest(cbr)
		}
	}
}