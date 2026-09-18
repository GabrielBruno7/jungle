package app

type Identity struct {
	ProviderID string

	Internal bool
}

func (i Identity) MayActAsProvider(providerID string) bool {
	if i.Internal {
		return true
	}
	return i.ProviderID != "" && i.ProviderID == providerID
}

func (i Identity) MayUseWalletOperations() bool {
	return i.Internal
}
