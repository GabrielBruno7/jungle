package router

import (
	"github.com/gin-gonic/gin"
	"go.uber.org/fx"
)

type Route interface {
	Method() string
	Pattern() string
	Handler() gin.HandlerFunc
}

func AsRoute(f any) any {
	return fx.Annotate(
		f,
		fx.As(new(Route)),
		fx.ResultTags(`group:"routes"`),
	)
}

type Set struct {
	Routes      []Route
	Middlewares []gin.HandlerFunc
}

func AsRouteSet(f any) any {
	return fx.Annotate(f, fx.ResultTags(`group:"routeSets"`))
}

func Simple(method, pattern string, handler gin.HandlerFunc) Route {
	return simpleRoute{method: method, pattern: pattern, handler: handler}
}

type simpleRoute struct {
	method  string
	pattern string
	handler gin.HandlerFunc
}

func (r simpleRoute) Method() string           { return r.method }
func (r simpleRoute) Pattern() string          { return r.pattern }
func (r simpleRoute) Handler() gin.HandlerFunc { return r.handler }

type Params struct {
	fx.In

	Engine    *gin.Engine
	Routes    []Route `group:"routes"`
	RouteSets []Set   `group:"routeSets"`
}

func Register(p Params) {
	for _, r := range p.Routes {
		p.Engine.Handle(r.Method(), r.Pattern(), r.Handler())
	}

	for _, set := range p.RouteSets {
		for _, r := range set.Routes {
			handlers := make([]gin.HandlerFunc, 0, len(set.Middlewares)+1)
			handlers = append(handlers, set.Middlewares...)
			handlers = append(handlers, r.Handler())
			p.Engine.Handle(r.Method(), r.Pattern(), handlers...)
		}
	}
}

var Module = fx.Module("router",
	fx.Invoke(Register),
)
