// This file is part of MinIO DirectPV
// Copyright (c) 2021, 2022 MinIO, Inc.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package metrics

import (
	"context"

	"github.com/minio/directpv/pkg/client"
	"github.com/minio/directpv/pkg/consts"
	"github.com/minio/directpv/pkg/device"
	"github.com/minio/directpv/pkg/k8s"
	"github.com/minio/directpv/pkg/types"
	"github.com/minio/directpv/pkg/volume"
	"github.com/minio/directpv/pkg/xfs"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

const tenantLabel = consts.AppName + ".min.io/tenant"

type metricsCollector struct {
	nodeID            string
	desc              *prometheus.Desc
	getDeviceByFSUUID func(fsuuid string) (string, error)
	getQuota          func(ctx context.Context, device, volumeID string) (quota *xfs.Quota, err error)
}

func newMetricsCollector(nodeID string) *metricsCollector {
	return &metricsCollector{
		nodeID:            nodeID,
		desc:              prometheus.NewDesc(consts.AppName+"_stats", "Statistics exposed by "+consts.AppPrettyName, nil, nil),
		getDeviceByFSUUID: device.GetDeviceByFSUUID,
		getQuota:          xfs.GetQuota,
	}
}

// Describe sends the super set of all possible descriptors of metrics
func (c *metricsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

func (mc *metricsCollector) publishVolumeStats(ctx context.Context, volume *types.Volume, ch chan<- prometheus.Metric) {
	device, err := mc.getDeviceByFSUUID(volume.Status.FSUUID)
	if err != nil {
		klog.ErrorS(
			err,
			"unable to find device by FSUUID; "+
				"either device is removed or run command "+
				"`sudo udevadm control --reload-rules && sudo udevadm trigger`"+
				"on the host to reload",
			"FSUUID", volume.Status.FSUUID)
		client.Eventf(
			volume, corev1.EventTypeWarning, "NodeStageVolume",
			"unable to find device by FSUUID %v; "+
				"either device is removed or run command "+
				"`sudo udevadm control --reload-rules && sudo udevadm trigger`"+
				" on the host to reload", volume.Status.FSUUID)
		return
	}
	quota, err := mc.getQuota(ctx, device, volume.Name)
	if err != nil {
		klog.ErrorS(err, "unable to get quota information", "volume", volume.Name)
		return
	}

	var tenantName string
	if labels := volume.GetLabels(); labels != nil {
		tenantName = labels[tenantLabel]
	}

	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(consts.AppName, "stats", "bytes_used"),
			"Total number of bytes used by the volume",
			[]string{"tenant", "volumeID", "node"}, nil),
		prometheus.GaugeValue,
		float64(quota.CurrentSpace), string(tenantName), volume.Name, volume.Status.NodeName,
	)

	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(
			prometheus.BuildFQName(consts.AppName, "stats", "bytes_total"),
			"Total number of bytes allocated to the volume",
			[]string{"tenant", "volumeID", "node"}, nil),
		prometheus.GaugeValue,
		float64(volume.Status.TotalCapacity), string(tenantName), volume.Name, volume.Status.NodeName,
	)
}

// Collect is called by Prometheus registry when collecting metrics.
func (c *metricsCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	resultCh, err := volume.ListVolumes(
		ctx,
		[]types.LabelValue{types.NewLabelValue(c.nodeID)},
		nil,
		nil,
		nil,
		k8s.MaxThreadCount,
	)
	if err != nil {
		klog.V(3).ErrorS(err, "unable to get volume list")
		return
	}

	for result := range resultCh {
		if result.Err != nil {
			return
		}

		if result.Volume.Status.ContainerPath != "" {
			c.publishVolumeStats(ctx, &result.Volume, ch)
		}
	}
}
