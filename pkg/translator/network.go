package translator

func (d *Docker) GetNetworks()                    {}
func (d *Docker) GetNetworkSummaries()            {}
func (d *Docker) CreateNetwork()                  {}
func (d *Docker) ConnectContainerToNetwork()      {}
func (d *Docker) DisconnectContainerFromNetwork() {}
func (d *Docker) DeleteNetwork()                  {}
func (d *Docker) NetworkPrune()                   {}

func (c *Cluster) GetNetworks()         {}
func (c *Cluster) GetNetworkSummaries() {}
func (c *Cluster) GetNetwork()          {}
func (c *Cluster) GetNetworksByName()   {}
func (c *Cluster) CreateNetwork()       {}
func (c *Cluster) RemoveNetwork()       {}
