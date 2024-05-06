package main

import (
    "context"
    "time"
    "log"
    "net/http"
    "os"
    "strings"

    "github.com/gin-gonic/gin"
    "github.com/borisce/ambulance-webapi/api"
    "github.com/borisce/ambulance-webapi/internal/ambulance_wl"
    "github.com/borisce/ambulance-webapi/internal/db_service"
    "github.com/prometheus/client_golang/prometheus/promhttp"
    "github.com/technologize/otel-go-contrib/otelginmetrics"
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/exporters/prometheus"
    "go.opentelemetry.io/otel/sdk/metric"
    "go.opentelemetry.io/otel/sdk/resource"
    semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
    "github.com/gin-contrib/cors"
)

// initialize OpenTelemetry instrumentations
func initTelemetry() error {
    ctx := context.Background()
    res, err := resource.New(ctx,
     resource.WithAttributes(semconv.ServiceNameKey.String("Ambulance WebAPI Service")),
     resource.WithAttributes(semconv.ServiceNamespaceKey.String("WAC Hospital")),
     resource.WithSchemaURL(semconv.SchemaURL),
     resource.WithContainer(),
    )
  
    if err != nil {
     return err
    }
  
    metricExporter, err := prometheus.New()
    if err != nil {
     return err
    }
  
    metricProvider := metric.NewMeterProvider(metric.WithReader(metricExporter), metric.WithResource(res))
    otel.SetMeterProvider(metricProvider)
    return nil
}

func main() {
    log.Printf("Server started")
    port := os.Getenv("AMBULANCE_API_PORT")
    if port == "" {
        port = "8080"
    }
    environment := os.Getenv("AMBULANCE_API_ENVIRONMENT")
    if !strings.EqualFold(environment, "production") { // case insensitive comparison
        gin.SetMode(gin.DebugMode)
    }
    engine := gin.New()
    engine.Use(gin.Recovery())

    // setup telemetry
    initTelemetry()    
    // instrument gin engine
    engine.Use(otelginmetrics.Middleware(
        "Ambulance WebAPI Service",
        // Custom attributes
        otelginmetrics.WithAttributes(func(serverName, route string, request *http.Request) []attribute.KeyValue {
            return append(otelginmetrics.DefaultAttributes(serverName, route, request))
        }),
    ))

    corsMiddleware := cors.New(cors.Config{
        AllowOrigins:     []string{"*"},
        AllowMethods:     []string{"GET", "PUT", "POST", "DELETE", "PATCH"},
        AllowHeaders:     []string{"Origin", "Authorization", "Content-Type"},
        ExposeHeaders:    []string{""},
        AllowCredentials: false,
        MaxAge: 12 * time.Hour,
    })
    engine.Use(corsMiddleware)

    // setup context update  middleware
    dbService := db_service.NewMongoService[ambulance_wl.Ambulance](db_service.MongoServiceConfig{})
    defer dbService.Disconnect(context.Background())
    engine.Use(func(ctx *gin.Context) {
        ctx.Set("db_service", dbService)
        ctx.Next()
    })

    // request routings
    ambulance_wl.AddRoutes(engine)
    engine.GET("/openapi", api.HandleOpenApi)

    // metrics endpoint
    promhandler := promhttp.Handler()
    engine.Any("/metrics", func(ctx *gin.Context) {
        promhandler.ServeHTTP(ctx.Writer, ctx.Request)
    })

    engine.Run(":" + port)
}