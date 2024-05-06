package ambulance_wl

import (
    "fmt"
    "log"
    "net/http"
    "time"
    "context"

    "github.com/gin-gonic/gin"
    "github.com/borisce/ambulance-webapi/internal/db_service"
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/metric"
    
)
var (
    dbMeter           = otel.Meter("waiting_list_access")
    dbTimeSpent       metric.Float64Counter
    waitingListLength = map[string]int64{}
)

// package initialization - called automaticaly by go runtime when package is used
func init() {
    // initialize OpenTelemetry instrumentations
    var err error
    dbTimeSpent, err = dbMeter.Float64Counter(
        "ambulance_wl_time_spent_in_db",
        metric.WithDescription("The time spent in the database requests"),
        metric.WithUnit("ms"),
    )

    if err != nil {
        panic(err)
    }
}
type ambulanceUpdater = func(
    ctx *gin.Context,
    ambulance *Ambulance,
) (updatedAmbulance *Ambulance, responseContent interface{}, status int)

func updateAmbulanceFunc(ctx *gin.Context, updater ambulanceUpdater) {
    value, exists := ctx.Get("db_service")
    if !exists {
        ctx.JSON(
            http.StatusInternalServerError,
            gin.H{
                "status":  "Internal Server Error",
                "message": "db_service not found",
                "error":   "db_service not found",
            })
        return
    }

    db, ok := value.(db_service.DbService[Ambulance])
    if !ok {
        ctx.JSON(
            http.StatusInternalServerError,
            gin.H{
                "status":  "Internal Server Error",
                "message": "db_service context is not of type db_service.DbService",
                "error":   "cannot cast db_service context to db_service.DbService",
            })
        return
    }

    ambulanceId := ctx.Param("ambulanceId")

    start := time.Now()
    ambulance, err := db.FindDocument(ctx, ambulanceId)
    dbTimeSpent.Add(ctx, float64(float64(time.Since(start)))/float64(time.Millisecond), metric.WithAttributes(
        attribute.String("operation", "find"),
        attribute.String("ambulance_id", ambulanceId),
        attribute.String("ambulance_name", ambulance.Name),
    ))

    switch err {
    case nil:
        // continue
    case db_service.ErrNotFound:
        ctx.JSON(
            http.StatusNotFound,
            gin.H{
                "status":  "Not Found",
                "message": "Ambulance not found",
                "error":   err.Error(),
            },
        )
        return
    default:
        ctx.JSON(
            http.StatusBadGateway,
            gin.H{
                "status":  "Bad Gateway",
                "message": "Failed to load ambulance from database",
                "error":   err.Error(),
            })
        return
    }

    if !ok {
        ctx.JSON(
            http.StatusInternalServerError,
            gin.H{
                "status":  "Internal Server Error",
                "message": "Failed to cast ambulance from database",
                "error":   "Failed to cast ambulance from database",
            })
        return
    }

    updatedAmbulance, responseObject, status := updater(ctx, ambulance)

    if updatedAmbulance != nil {
        start := time.Now()
        err = db.UpdateDocument(ctx, ambulanceId, updatedAmbulance)
        // update metrics
        dbTimeSpent.Add(ctx, float64(float64(time.Since(start)))/float64(time.Millisecond), metric.WithAttributes(
            attribute.String("operation", "update"),
            attribute.String("ambulance_id", ambulanceId),
            attribute.String("ambulance_name", ambulance.Name),
        ))

        // demonstration of possible handling of async instruments:
        // not really an operational metric, it would be more of a business metric/KPI.
        // also UpDownCounter may be of better use in practical cases.
        if _, ok := waitingListLength[ambulanceId]; !ok {
            newGauge, err := dbMeter.Int64ObservableGauge(
                fmt.Sprintf("%v_waiting_patients", ambulanceId),
                metric.WithDescription(fmt.Sprintf("The length of the waiting list for the ambulance %v", ambulance.Name)),
                metric.WithUnit("{patient}"),
            )
            if err != nil {
                log.Printf("Failed to create waiting list length gauge for ambulance %v: %v", ambulanceId, err)
            }
            waitingListLength[ambulanceId] = 0

            _, err = dbMeter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
                // we could have looked up the ambulance in the database here, but we already have it in memory
                // so use the latest snapshots to update the gauge
                o.ObserveInt64(newGauge, waitingListLength[ambulanceId])
                return nil
            }, newGauge)

            if err != nil {
                log.Printf("Failed to register callback for waiting list length gauge for ambulance %v: %v", ambulanceId, err)
            }
        }

        // set the gauge snapshot
        waitingListLength[ambulanceId] = int64(len(updatedAmbulance.WaitingList))
        
    } else {
        err = nil // redundant but for clarity
    }

    switch err {
    case nil:
        if responseObject != nil {
            ctx.JSON(status, responseObject)
        } else {
            ctx.AbortWithStatus(status)
        }
    case db_service.ErrNotFound:
        ctx.JSON(
            http.StatusNotFound,
            gin.H{
                "status":  "Not Found",
                "message": "Ambulance was deleted while processing the request",
                "error":   err.Error(),
            },
        )
    default:
        ctx.JSON(
            http.StatusBadGateway,
            gin.H{
                "status":  "Bad Gateway",
                "message": "Failed to update ambulance in database",
                "error":   err.Error(),
            })
    }

}