/* SPDX-License-Identifier: GPL-3.0-or-later */
#include "common.h"

#include <pthread.h>
#include <signal.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

#include "hal_thread.h"
#include "hal_time.h"

static Fixture fx;
/* Guards the fixture state: handlers run on one thread per connection. */
static pthread_mutex_t stateLock = PTHREAD_MUTEX_INITIALIZER;
static volatile sig_atomic_t running = 1;

static void onSignal(int sig)
{
    (void)sig;
    running = 0;
}

static cJSON* event(const char* name)
{
    cJSON* e = cJSON_CreateObject();
    cJSON_AddStringToObject(e, "event", name);
    return e;
}

static void addPeer(cJSON* e, IMasterConnection connection)
{
    char addr[64];
    addr[0] = 0;
    IMasterConnection_getPeerAddress(connection, addr, sizeof(addr));
    cJSON_AddStringToObject(e, "peer", addr);
}

/* Mirrors asdu with one of the "unknown ..." causes and the P/N bit set. */
static void reject(IMasterConnection connection, CS101_ASDU asdu, CS101_CauseOfTransmission cot)
{
    CS101_ASDU_setCOT(asdu, cot);
    CS101_ASDU_setNegative(asdu, true);
    IMasterConnection_sendASDU(connection, asdu);
}

static bool isCAAllowed(void* parameter, int ca)
{
    (void)parameter;
    return ca == fx.commonAddress || ca == 0xFFFF;
}

static bool connectionRequestHandler(void* parameter, const char* ipAddress)
{
    (void)parameter;
    (void)ipAddress;
    return true;
}

static void connectionEventHandler(void* parameter, IMasterConnection connection, CS104_PeerConnectionEvent ev)
{
    (void)parameter;
    static const char* names[] = { "connection-opened", "connection-closed", "data-transfer-started",
        "data-transfer-stopped" };
    if ((int)ev < 0 || (int)ev > 3)
        return;
    cJSON* e = event(names[ev]);
    addPeer(e, connection);
    emitJson(e);
}

/* Sends the points selected by counters (true: integrated totals, false:
 * everything else) in ascending address order, packing consecutive points of
 * one type into one ASDU. */
static void sendPoints(IMasterConnection connection, CS101_CauseOfTransmission cot, int oa, bool counters)
{
    CS101_AppLayerParameters params = IMasterConnection_getApplicationLayerParameters(connection);
    CS101_ASDU asdu = NULL;
    IEC60870_5_TypeID current = 0;

    for (int i = 0; i < fx.pointCount; i++) {
        Point* p = &fx.points[i];
        if ((p->type == M_IT_NA_1) != counters)
            continue;
        InformationObject io = pointToObject(p, NULL);
        if (asdu != NULL && (p->type != current || !CS101_ASDU_addInformationObject(asdu, io))) {
            IMasterConnection_sendASDU(connection, asdu);
            CS101_ASDU_destroy(asdu);
            asdu = NULL;
        }
        if (asdu == NULL) {
            asdu = CS101_ASDU_create(params, false, cot, oa, fx.commonAddress, false, false);
            current = p->type;
            CS101_ASDU_addInformationObject(asdu, io);
        }
        InformationObject_destroy(io);
    }
    if (asdu != NULL) {
        IMasterConnection_sendASDU(connection, asdu);
        CS101_ASDU_destroy(asdu);
    }
}

static bool interrogationHandler(void* parameter, IMasterConnection connection, CS101_ASDU asdu, uint8_t qoi)
{
    (void)parameter;
    cJSON* e = event("interrogation");
    cJSON_AddNumberToObject(e, "commonAddress", CS101_ASDU_getCA(asdu));
    cJSON_AddNumberToObject(e, "qoi", qoi);
    cJSON_AddBoolToObject(e, "accepted", qoi == IEC60870_QOI_STATION);
    emitJson(e);

    /* A broadcast request is answered with the station's own address. */
    CS101_ASDU_setCA(asdu, fx.commonAddress);
    if (qoi != IEC60870_QOI_STATION) {
        IMasterConnection_sendACT_CON(connection, asdu, true);
        return true;
    }
    IMasterConnection_sendACT_CON(connection, asdu, false);
    pthread_mutex_lock(&stateLock);
    sendPoints(connection, CS101_COT_INTERROGATED_BY_STATION, CS101_ASDU_getOA(asdu), false);
    pthread_mutex_unlock(&stateLock);
    IMasterConnection_sendACT_TERM(connection, asdu);
    return true;
}

static bool counterInterrogationHandler(void* parameter, IMasterConnection connection, CS101_ASDU asdu,
    QualifierOfCIC qcc)
{
    (void)parameter;
    /* Only "general request counter" with "read" (no freeze, no reset). */
    bool accepted = qcc == (IEC60870_QCC_RQT_GENERAL | IEC60870_QCC_FRZ_READ);
    cJSON* e = event("counter-interrogation");
    cJSON_AddNumberToObject(e, "commonAddress", CS101_ASDU_getCA(asdu));
    cJSON_AddNumberToObject(e, "qcc", qcc);
    cJSON_AddBoolToObject(e, "accepted", accepted);
    emitJson(e);

    CS101_ASDU_setCA(asdu, fx.commonAddress);
    if (!accepted) {
        IMasterConnection_sendACT_CON(connection, asdu, true);
        return true;
    }
    IMasterConnection_sendACT_CON(connection, asdu, false);
    pthread_mutex_lock(&stateLock);
    sendPoints(connection, CS101_COT_REQUESTED_BY_GENERAL_COUNTER, CS101_ASDU_getOA(asdu), true);
    pthread_mutex_unlock(&stateLock);
    IMasterConnection_sendACT_TERM(connection, asdu);
    return true;
}

static bool readHandler(void* parameter, IMasterConnection connection, CS101_ASDU asdu, int ioa)
{
    (void)parameter;
    pthread_mutex_lock(&stateLock);
    Point* p = Fixture_findPoint(&fx, ioa);
    InformationObject io = p ? pointToObject(p, NULL) : NULL;
    pthread_mutex_unlock(&stateLock);

    cJSON* e = event("read");
    cJSON_AddNumberToObject(e, "ioa", ioa);
    cJSON_AddBoolToObject(e, "accepted", io != NULL);
    emitJson(e);

    if (io == NULL) {
        reject(connection, asdu, CS101_COT_UNKNOWN_IOA);
        return true;
    }
    CS101_ASDU reply = CS101_ASDU_create(IMasterConnection_getApplicationLayerParameters(connection), false,
        CS101_COT_REQUEST, CS101_ASDU_getOA(asdu), fx.commonAddress, false, false);
    CS101_ASDU_addInformationObject(reply, io);
    IMasterConnection_sendASDU(connection, reply);
    CS101_ASDU_destroy(reply);
    InformationObject_destroy(io);
    return true;
}

static bool clockSyncHandler(void* parameter, IMasterConnection connection, CS101_ASDU asdu, CP56Time2a newTime)
{
    (void)parameter;
    (void)connection;
    (void)asdu;
    char buf[40];
    formatTime(CP56Time2a_toMsTimestamp(newTime), buf, sizeof(buf));
    cJSON* e = event("clock-sync");
    cJSON_AddStringToObject(e, "time", buf);
    emitJson(e);
    return true; /* lib60870 sends the activation confirmation */
}

/* Decodes the command object of asdu. Returns false when the value is one
 * the command type does not permit. */
static bool decodeCommand(InformationObject io, IEC60870_5_TypeID type, double* value, bool* select, cJSON* e)
{
    switch (type) {
    case C_SC_NA_1:
        *value = SingleCommand_getState((SingleCommand)io) ? 1 : 0;
        *select = SingleCommand_isSelect((SingleCommand)io);
        cJSON_AddBoolToObject(e, "value", *value != 0);
        return true;
    case C_DC_NA_1:
        *value = DoubleCommand_getState((DoubleCommand)io);
        *select = DoubleCommand_isSelect((DoubleCommand)io);
        cJSON_AddNumberToObject(e, "value", *value);
        return *value == 1 || *value == 2;
    case C_RC_NA_1:
        *value = StepCommand_getState((StepCommand)io);
        *select = StepCommand_isSelect((StepCommand)io);
        cJSON_AddNumberToObject(e, "value", *value);
        return *value == IEC60870_STEP_LOWER || *value == IEC60870_STEP_HIGHER;
    case C_SE_NA_1:
        *value = normalizedToRaw(SetpointCommandNormalized_getValue((SetpointCommandNormalized)io));
        *select = SetpointCommandNormalized_isSelect((SetpointCommandNormalized)io);
        cJSON_AddNumberToObject(e, "value", *value);
        return true;
    case C_SE_NB_1:
        *value = SetpointCommandScaled_getValue((SetpointCommandScaled)io);
        *select = SetpointCommandScaled_isSelect((SetpointCommandScaled)io);
        cJSON_AddNumberToObject(e, "value", *value);
        return true;
    case C_SE_NC_1:
        *value = (double)SetpointCommandShort_getValue((SetpointCommandShort)io);
        *select = SetpointCommandShort_isSelect((SetpointCommandShort)io);
        cJSON_AddNumberToObject(e, "value", *value);
        return true;
    default:
        return false;
    }
}

/* Applies an executed command to its target. Returns false when the target
 * cannot take the value (a step position at its limit). */
static bool applyCommand(Command* c, double value)
{
    Point* t = &fx.points[c->target];
    if (c->type == C_RC_NA_1) {
        double next = t->value + (value == IEC60870_STEP_HIGHER ? 1 : -1);
        if (next < -64 || next > 63)
            return false;
        t->value = next;
        return true;
    }
    t->value = value;
    return true;
}

static bool asduHandler(void* parameter, IMasterConnection connection, CS101_ASDU asdu)
{
    (void)parameter;
    IEC60870_5_TypeID wire = CS101_ASDU_getTypeID(asdu);
    IEC60870_5_TypeID type = baseType(wire);
    if (type < C_SC_NA_1 || type > C_SE_NC_1)
        return false; /* lib60870 answers "unknown type identification" */

    CS101_CauseOfTransmission cot = CS101_ASDU_getCOT(asdu);
    if (cot != CS101_COT_ACTIVATION && cot != CS101_COT_DEACTIVATION) {
        reject(connection, asdu, CS101_COT_UNKNOWN_COT);
        return true;
    }
    InformationObject io = CS101_ASDU_getElement(asdu, 0);
    if (io == NULL)
        return false;
    int ioa = InformationObject_getObjectAddress(io);

    cJSON* e = event("command");
    cJSON_AddStringToObject(e, "type", typeName(wire));
    cJSON_AddNumberToObject(e, "ioa", ioa);
    cJSON_AddNumberToObject(e, "cot", cot);

    pthread_mutex_lock(&stateLock);
    Command* c = Fixture_findCommand(&fx, ioa);
    if (c == NULL || c->type != type) {
        pthread_mutex_unlock(&stateLock);
        InformationObject_destroy(io);
        cJSON_AddStringToObject(e, "outcome", "unknown-ioa");
        emitJson(e);
        reject(connection, asdu, CS101_COT_UNKNOWN_IOA);
        return true;
    }

    double value = 0;
    bool select = false;
    bool valid = decodeCommand(io, type, &value, &select, e);
    InformationObject_destroy(io);
    cJSON_AddBoolToObject(e, "select", select);
    CS101_ASDU_setCA(asdu, fx.commonAddress);

    const char* outcome;
    InformationObject report = NULL;
    if (cot == CS101_COT_DEACTIVATION) {
        c->selected = false;
        outcome = "deactivated";
    } else if (!valid) {
        outcome = "rejected";
    } else if (select) {
        c->selected = true;
        outcome = "selected";
    } else if (c->selectRequired && !c->selected) {
        outcome = "rejected";
    } else {
        c->selected = false;
        if (applyCommand(c, value)) {
            struct sCP56Time2a now;
            CP56Time2a_createFromMsTimestamp(&now, Hal_getTimeInMs());
            report = pointToObject(&fx.points[c->target], &now);
            outcome = "executed";
        } else {
            outcome = "rejected";
        }
    }
    int reportCause = c->reportCause;
    pthread_mutex_unlock(&stateLock);

    cJSON_AddStringToObject(e, "outcome", outcome);
    emitJson(e);

    if (strcmp(outcome, "deactivated") == 0) {
        CS101_ASDU_setCOT(asdu, CS101_COT_DEACTIVATION_CON);
        CS101_ASDU_setNegative(asdu, false);
        IMasterConnection_sendASDU(connection, asdu);
    } else if (strcmp(outcome, "rejected") == 0) {
        IMasterConnection_sendACT_CON(connection, asdu, true);
    } else {
        IMasterConnection_sendACT_CON(connection, asdu, false);
        if (report != NULL) {
            /* Confirmation, then the new state of the target, then termination. */
            CS101_ASDU r = CS101_ASDU_create(IMasterConnection_getApplicationLayerParameters(connection), false,
                (CS101_CauseOfTransmission)reportCause, 0, fx.commonAddress, false, false);
            CS101_ASDU_addInformationObject(r, report);
            IMasterConnection_sendASDU(connection, r);
            CS101_ASDU_destroy(r);
            InformationObject_destroy(report);
            IMasterConnection_sendACT_TERM(connection, asdu);
        }
    }
    return true;
}

int runServer(Args* args)
{
    const char* fixturePath = argString(args, "--fixture", DEFAULT_FIXTURE);
    const char* bindAddress = argString(args, "--bind-address", "0.0.0.0");
    const char* readyFile = argString(args, "--ready-file", DEFAULT_READY_FILE);
    int port;
    if (!argInt(args, "--port", 2404, &port) || port < 1 || port > 65535)
        return EXIT_USAGE;

    CS104_Slave slave = CS104_Slave_create(1024, 1024);
    if (!applyApciArgs(args, CS104_Slave_getConnectionParameters(slave)))
        return EXIT_USAGE;
    if (argUnknown(args) != NULL) {
        logf_("server: unknown argument %s", argUnknown(args));
        return EXIT_USAGE;
    }

    char err[256];
    if (!Fixture_load(fixturePath, &fx, err, sizeof(err))) {
        logf_("fixture %s: %s", fixturePath, err);
        return EXIT_FIXTURE_INVALID;
    }
    unlink(readyFile);

    CS104_Slave_setLocalAddress(slave, bindAddress);
    CS104_Slave_setLocalPort(slave, port);
    /* Every connection is its own redundancy group: independent sessions. */
    CS104_Slave_setServerMode(slave, CS104_MODE_CONNECTION_IS_REDUNDANCY_GROUP);
    CS104_Slave_setMaxOpenConnections(slave, 16);
    CS104_Slave_setAllowedCAHandler(slave, isCAAllowed, NULL);
    CS104_Slave_setConnectionRequestHandler(slave, connectionRequestHandler, NULL);
    CS104_Slave_setConnectionEventHandler(slave, connectionEventHandler, NULL);
    CS104_Slave_setInterrogationHandler(slave, interrogationHandler, NULL);
    CS104_Slave_setCounterInterrogationHandler(slave, counterInterrogationHandler, NULL);
    CS104_Slave_setReadHandler(slave, readHandler, NULL);
    CS104_Slave_setClockSyncHandler(slave, clockSyncHandler, NULL);
    CS104_Slave_setASDUHandler(slave, asduHandler, NULL);

    struct sigaction sa;
    memset(&sa, 0, sizeof(sa));
    sa.sa_handler = onSignal;
    sigaction(SIGTERM, &sa, NULL);
    sigaction(SIGINT, &sa, NULL);

    CS104_Slave_start(slave);
    if (!CS104_Slave_isRunning(slave)) {
        logf_("cannot listen on %s:%d", bindAddress, port);
        CS104_Slave_destroy(slave);
        return EXIT_OPERATION_FAILED;
    }

    FILE* f = fopen(readyFile, "w");
    if (f != NULL) {
        fputs("ready\n", f);
        fclose(f);
    } else {
        logf_("warning: cannot write ready file %s", readyFile);
    }
    char address[96];
    snprintf(address, sizeof(address), "%s:%d", bindAddress, port);
    cJSON* e = event("ready");
    cJSON_AddStringToObject(e, "adapter", ADAPTER_NAME);
    cJSON_AddStringToObject(e, "address", address);
    cJSON_AddStringToObject(e, "fixture", fx.name);
    cJSON_AddNumberToObject(e, "commonAddress", fx.commonAddress);
    emitJson(e);

    while (running && CS104_Slave_isRunning(slave))
        Thread_sleep(50);

    unlink(readyFile);
    CS104_Slave_stop(slave);
    CS104_Slave_destroy(slave);
    emitJson(event("stopped"));
    return EXIT_OK;
}
