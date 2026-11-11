package codexbinding

import "github.com/yusing/mekugi/modules/threadcontrol/control"

// BindFeatures supplies the root launch's owner before runtime admission.
func (d *Driver) BindFeatures(source control.FeatureSource) {
	d.mu.Lock()
	d.features = source
	d.mu.Unlock()
}

func (d *Driver) featureSource() control.FeatureSource {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.features
}

func (d *Driver) bindFeatureUse(thread string) {
	if source := d.featureSource(); source != nil {
		d.control.BindFeatureUse(thread, source)
	}
}

// FeatureUse queues a read on the existing lifecycle worker. The host event loop
// remains free to acknowledge its RPC; no inference or runtime admission occurs.
func (d *Driver) FeatureUse(thread string) <-chan control.FeatureSnapshot {
	result := make(chan control.FeatureSnapshot, 1)
	d.enqueue(hostObservation{thread: thread, method: "featureUse/read", features: result})
	return result
}
