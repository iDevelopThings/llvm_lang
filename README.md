
# What to even put here?

Purely vibe coded go/c like language written over a weekend or so by claude/cursor/codex.
Did my best to keep them on track and architecture in a semi-decent state.
Tried to keep perf in mind compiler side due to LLVM/cgo overhead.

For the most part it's mostly functional, i wrote a little raylib game using c bindings and perf was great, some of the syntax is a bit verbose in some places maybe.

Check out:
- [Examples](examples) for some of the things you can do with it.
- [Docs Overview](docs/README.md) for a guided tour of the language's features.
- [Feature Index](docs/feature-index.md) for a quick overview of the language's capabilities.


Other bits to know: 
- Had to extend go llvm with orcjit/windows support, so it's just here in the repo.
- I'm on windows, currently it depends mostly on msys2 mingw64 gcc llvm 22.1.7 - I'm sorry if you don't fit in this area, i was vibin, you know how it be
- Because of the orc jit support, it also has hot reload support(great for playing around making games)


# "Gib syntax"

Here's the raylib game
Don't expect this to be insane code practices, i hacked it together to validate the language and test the features, coming back and fourth, didn't pay too much attention to the code quality, but it works and is a good example of the language's capabilities.

```llx

import "std:scheduler"
import "std:collections"
import "std:mathutil"
import "std:vectors"
import "std:rect"
import "std:rand"

// game.llx - hot-reloadable raylib game loop, driven by `llvmc -watch`.
// Init opens the window once; Frame runs every tick, hot-swapped whenever
// this file changes on disk
// see CODEGEN.md's "-watch" section on reset-on-reload).

//
// Run via:
// llvmc.exe -watch -L F:/GameDev/Raylib/src -l raylib F:/GameDev/llx-raylib-test/game.llx
//

struct Player {
	Location  vectors.Vector2
	MoveSpeed f32

	constructor() {
		this.MoveSpeed = 200
	}
}
struct Projectile {
	Location     vectors.Vector2
	Velocity     vectors.Vector2
	StartDestroy bool
}

var sched scheduler.Scheduler

var projectileTex Texture

var camera Camera2D = Camera2D{
	offset:   vectors.Vector2{},
	target:   vectors.Vector2{},
	rotation: 0,
	zoom:     1,
}

var p *Player

var projectiles collections.SlotMap[Projectile]
var collision collections.SlotMap[rect.Rect]

func Init() {
	if !IsWindowReady() {
		InitWindow(640, 480, cstring("llvm_lang + raylib (hot reload)"))
		SetTargetFPS(240)
	}

	camera.offset.X = f32(GetScreenWidth()) / 2
	camera.offset.Y = f32(GetScreenHeight()) / 2

	p = new Player()
	projectiles = collections.NewSlotMap[Projectile]()
	collision = collections.NewSlotMap[rect.Rect]()

	projectileTex = LoadTexture(cstring("projectile.png"))

	collision.Insert(rect.Rect{vectors.Vector2{100, 100}, vectors.Vector2{80, 80}})
	collision.Insert(rect.Rect{vectors.Vector2{300, 200}, vectors.Vector2{80, 80}})
	collision.Insert(rect.Rect{vectors.Vector2{500, 300}, vectors.Vector2{80, 80}})
}

func Frame() int {
	if WindowShouldClose() {
		return 1
	}

	BeginDrawing()
	ClearBackground(BLACK)
	DrawFPS(10, 10)

	BeginMode2D(camera)

	GameTick(GetFrameTime())

	for handle := range collections.Handles(&collision) {
		h := collision.Get(handle)

		DrawRectangleRec(Rectangle{
			h.Position.X,
			h.Position.Y,
			h.Size.X,
			h.Size.Y,
		}, BLUE)
	}

	DrawCircleV(p.Location, 20, RED)

	wPos := GetScreenToWorld2D(GetMousePosition(), camera)
	DrawCircleV(wPos, 5, RED)

	for handle := range collections.Handles(&projectiles) {
		h := projectiles.GetPtr(handle)

		DrawTextureV(projectileTex, h.Location, WHITE)
	}


	EndMode2D()

	EndDrawing()

	return 0
}

func GameTick(d f32) {
	sched.Tick(f64(d))

	movInput := vectors.Vector2{}
	if IsKeyDown(KEY_A): movInput.X = -1
	if IsKeyDown(KEY_D): movInput.X = 1
	if IsKeyDown(KEY_W): movInput.Y = -1
	if IsKeyDown(KEY_S): movInput.Y = 1

	scroll := GetMouseWheelMove()
	if scroll != 0 {
		print(scroll)
		camera.zoom += mathutil.Clamp[f32](scroll * 0.2, -4, 4)
		camera.zoom = mathutil.Clamp[f32](camera.zoom, 0.1, 10)
	}

	newLoc := p.Location + movInput * p.MoveSpeed * d
	colDir, ok := CheckColliding(rect.Rect{
		Position: newLoc - vectors.Vector2{20, 20},
		Size:     vectors.Vector2{40, 40},
	})
	if ok {
		print("colliding")
	} else {
		p.Location = newLoc
	}

	camera.target = p.Location

	if IsMouseButtonDown(MOUSE_BUTTON_LEFT) {
		e := new scheduler.Entry{}
		e.Handle = FireShot(&e.NextWait)
		sched.Schedule(e)
	}

}

async func FireShot(nextWait *f64) {
	wPos := GetScreenToWorld2D(GetMousePosition(), camera)
	dir := wPos - p.Location
	dir = dir.Normalize()
	dir = dir * 1000

	handle := projectiles.Insert(Projectile{
		Location: p.Location,
		Velocity: dir,
	})

	proj := projectiles.GetPtr(handle)

	var lifeTime f32 = 1.0
	var decay f32 = 0.0

	for {
		ft := GetFrameTime()
		lifeTime -= ft
		if lifeTime <= 0 {
			break
		}

		proj.Location = proj.Location + proj.Velocity * ft

		colDir, collided := CheckColliding(rect.Rect{
			Position: proj.Location - vectors.Vector2{5, 5},
			Size:     vectors.Vector2{10, 10},
		})

		if collided {
			break
		}

		*nextWait = 0
		await
	}


	CreatePixelExplosion(proj.Location)
	projectiles.Remove(handle)

}


func CheckColliding(a rect.Rect) (vectors.Vector2, bool) {
	intersected := false
	dir := vectors.Vector2{0, 0}
	for handle := range collections.Handles(&collision) {
		h := collision.Get(handle)
		if a.Intersects(h) {
			i, _ := a.Intersection(h)
			dir = a.Center() - i.Center()
			intersected = true
			break
		}
	}
	return dir, intersected
}

func CreatePixelExplosion(pos vectors.Vector2) {
	for i := 0; i < 20; i++ {
		e := new scheduler.Entry{}
		e.Handle = CreatePixel(&e.NextWait, pos)
		sched.Schedule(e)
	}
}
async func CreatePixel(nextWait *f64, pos vectors.Vector2) {
	var lifeTime f32 = 0.5

	vel := vectors.Vector2{
		f32(rand.FloatRange(-100, 100)),
		f32(rand.FloatRange(-100, 100)),
	}

	for {
		ft := GetFrameTime()
		lifeTime -= ft
		if lifeTime <= 0 {
			break
		}

		pos = pos + vel * ft

		DrawCircleV(pos, 2, YELLOW)

		*nextWait = 0
		await
	}
}

```
