package app

import (
	"go.uber.org/fx"
)

type InstanceID string

var Module = fx.Module("app",
	fx.Provide(
		fx.Annotate(func() SystemClock { return SystemClock{} }, fx.As(new(Clock))),

		DefaultReferencePolicy,
		DefaultPublishPolicy,

		NewOpenWallet,
		NewProcessWager,
		NewQueries,
		NewResolveReferences,
		NewPublishOutbox,
	),
)
