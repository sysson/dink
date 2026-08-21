package translator

func (t *Translator) GetNetworks()                    {}
func (t *Translator) GetNetworkSummaries()            {}
func (t *Translator) CreateNetwork()                  {}
func (t *Translator) ConnectContainerToNetwork()      {}
func (t *Translator) DisconnectContainerFromNetwork() {}
func (t *Translator) DeleteNetwork()                  {}
func (t *Translator) NetworkPrune()                   {}

func (t *Translator) GetClusterNetworks()         {}
func (t *Translator) GetClusterNetworkSummaries() {}
func (t *Translator) GetClusterNetwork()          {}
func (t *Translator) GetClusterNetworksByName()   {}
func (t *Translator) CreateClusterNetwork()       {}
func (t *Translator) RemoveClusterNetwork()       {}
