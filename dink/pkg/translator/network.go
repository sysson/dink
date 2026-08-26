package translator

func (d *Docker) GetNetworks()                    {}
func (d *Docker) GetNetworkSummaries()            {}
func (d *Docker) CreateNetwork()                  {}
func (d *Docker) ConnectContainerToNetwork()      {}
func (d *Docker) DisconnectContainerFromNetwork() {}
func (d *Docker) DeleteNetwork()                  {}
func (d *Docker) NetworkPrune()                   {}

func (s *Swarm) GetNetworks()         {}
func (s *Swarm) GetNetworkSummaries() {}
func (s *Swarm) GetNetwork()          {}
func (s *Swarm) GetNetworksByName()   {}
func (s *Swarm) CreateNetwork()       {}
func (s *Swarm) RemoveNetwork()       {}
