package sinks

import (
	"context"
	"time"

	"github.com/cybertec-postgresql/pgwatch/v3/internal/log"
	"github.com/cybertec-postgresql/pgwatch/v3/internal/metrics"
	pb "github.com/cybertec-postgresql/pgwatch/v3/internal/sinks/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

func NewRPCWriter(ctx context.Context, host string) (*RPCWriter, error) {
	conn, err := grpc.NewClient(host, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}

	client := pb.NewReceiverClient(conn)
	rw := &RPCWriter{
		ctx: ctx, 
		conn: conn, 
		client: client,
	}
	go rw.watchCtx()

	return rw, nil
}

// Sends Measurement Message to RPC Sink
func (rw *RPCWriter) Write(msg metrics.MeasurementEnvelope) error {
	if rw.ctx.Err() != nil {
		return rw.ctx.Err()
	}
	dataLength := len(msg.Data)

	// convert map[string]any to protobuf's equivalent for it structpb
	measurements := make([]*structpb.Struct, 0, dataLength)
	for _, item := range msg.Data {
		st, err := structpb.NewStruct(item) 
		if err != nil {
			continue
		}
		measurements = append(measurements, st)
	}

	envelope := &pb.MeasurementEnvelope{
		DBName: msg.DBName,
		MetricName: msg.MetricName,
		CustomTags: msg.CustomTags,
		Data: measurements,
	}

	t1 := time.Now()
	reply, err := rw.client.UpdateMeasurements(rw.ctx, envelope)
	if err != nil {
		return err
	}

	diff := time.Since(t1)
	log.GetLogger(rw.ctx).WithField("rows", dataLength).WithField("elapsed", diff).Info("measurements written")
	logMsg := reply.GetLogmsg()
	if len(logMsg) > 0 {
		log.GetLogger(rw.ctx).Info(logMsg)
	}
	return nil
}

func (rw *RPCWriter) SyncMetric(dbUnique, metricName string, op SyncOp) error {
	req := &pb.SyncReq{
		DBName: dbUnique,	
		MetricName: metricName,
		Operation: pb.SyncOp(op),
	}
	reply, err := rw.client.SyncMetric(rw.ctx, req)
	if err != nil {
		return nil
	}
	
	logMsg := reply.GetLogmsg()
	if len(logMsg) > 0 {
		log.GetLogger(rw.ctx).Info(logMsg)
	}
	return nil
}

func (rw *RPCWriter) watchCtx() {
	<-rw.ctx.Done()
	rw.conn.Close()
}
