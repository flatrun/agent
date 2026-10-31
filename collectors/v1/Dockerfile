ARG COLLECTOR_BASE
FROM ${COLLECTOR_BASE}
WORKDIR /work
COPY --chmod=0444 access.py inventory.py /collectors/
USER 65532:65532
ENTRYPOINT ["python3", "-I"]
