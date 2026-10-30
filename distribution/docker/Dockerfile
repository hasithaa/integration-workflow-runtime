# Sketch: one image, the profile chosen at start (WORKFLOW_RUNTIME_PROFILES).
# Build after `make build sbom GOOS=linux GOARCH=<arch>` so the binaries and their SBOMs match the image.
FROM gcr.io/distroless/static-debian12
COPY build/workflow-engine build/workflow-engine-sql build/workflow-api /opt/workflow-runtime/bin/
COPY distribution/conf/ /opt/workflow-runtime/conf/
COPY LICENSE NOTICE /opt/workflow-runtime/
COPY third-party/ /opt/workflow-runtime/third-party/
COPY build/sbom/ /opt/workflow-runtime/sbom/
ENTRYPOINT ["/opt/workflow-runtime/bin/workflow-engine"]
