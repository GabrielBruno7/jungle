package router

import (
	"github.com/gin-gonic/gin"
	"go.uber.org/fx"
)

// Route is anything that wants to be mounted on the gin engine. Feature
// packages implement this and provide it through AsRoute so it lands in
// the shared "routes" value group without router knowing about them.
type Route interface {
	Method() string
	Pattern() string
	Handler() gin.HandlerFunc
}

// AsRoute annotates a Route constructor so its result feeds the "routes"
// value group consumed by Register.
func AsRoute(f any) any {
	return fx.Annotate(
		f,
		fx.As(new(Route)),
		fx.ResultTags(`group:"routes"`),
	)
}

// Params collects every Route registered across all feature modules.
type Params struct {
	fx.In

	Engine *gin.Engine
	Routes []Route `group:"routes"`
}

// Register mounts every Route on the gin engine.
func Register(p Params) {
	for _, r := range p.Routes {
		p.Engine.Handle(r.Method(), r.Pattern(), r.Handler())
	}
}

// Module wires route registration into the fx graph. It must be composed
// after every feature module that provides routes.
var Module = fx.Module("router",
	fx.Invoke(Register),
)
