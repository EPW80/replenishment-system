# Multi-stage build for CadenceOS.
#
# The image carries all five binaries plus scripts/nightly.sh, because the Coolify
# scheduled task (docs/SCHEDULED_JOBS.md) runs in this same image -- one image, one
# build, one commit SHA for the service and its batch jobs alike.

FROM golang:1.25.7-alpine AS build

WORKDIR /src

# Dependencies first, so a source-only change does not re-download the module cache.
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .

# CGO off so the binaries are static and run on a base with no libc to match.
# Stripped (-s -w) because nothing here reads its own symbol table.
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags='-s -w' -o /out/ ./cmd/...

FROM alpine:3.22

# ca-certificates: cmd/notify calls the Postmark API over HTTPS and would fail
# certificate verification without them.
#
# tzdata: NOT optional, and its absence fails silently. Service.today calls
# time.LoadLocation with the schedule's own timezone and falls back to UTC on error
# -- deliberately, so a malformed stored timezone cannot block a cancellation. In an
# image with no zoneinfo database every LoadLocation fails, so that fallback fires for
# every schedule and the whole service quietly computes dates in UTC instead of the
# customer's timezone. Nothing errors; the dates are just wrong near midnight.
RUN apk add --no-cache ca-certificates tzdata

# Unprivileged: nothing here needs root, and the service only binds 8080.
RUN adduser -D -u 10001 cadenceos

WORKDIR /app

# On PATH, so scripts/nightly.sh resolves each job with 'command -v' rather than
# falling back to 'go run' -- there is no Go toolchain in this stage.
COPY --from=build /out/ /usr/local/bin/
COPY scripts/ /app/scripts/

USER cadenceos

EXPOSE 8080

# Overridden by the Coolify scheduled task, which runs ./scripts/nightly.sh.
CMD ["cadenceos"]
