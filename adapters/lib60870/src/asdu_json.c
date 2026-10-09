/* SPDX-License-Identifier: GPL-3.0-or-later */
#include "common.h"

#include <stdio.h>

static void addTime(cJSON* o, CP56Time2a t)
{
    char buf[40];
    formatTime(CP56Time2a_toMsTimestamp(t), buf, sizeof(buf));
    cJSON_AddStringToObject(o, "time", buf);
    if (CP56Time2a_isInvalid(t))
        cJSON_AddTrueToObject(o, "timeInvalid");
}

static void addNormalized(cJSON* o, float value)
{
    int raw = normalizedToRaw(value);
    cJSON_AddNumberToObject(o, "value", raw);
    cJSON_AddNumberToObject(o, "normalized", (double)raw / 32768.0);
}

static void addCommand(cJSON* o, bool select, int qualifier)
{
    cJSON_AddBoolToObject(o, "select", select);
    cJSON_AddNumberToObject(o, "qualifier", qualifier);
}

static void addCounter(cJSON* o, BinaryCounterReading bcr)
{
    cJSON_AddNumberToObject(o, "value", BinaryCounterReading_getValue(bcr));
    cJSON_AddNumberToObject(o, "sequence", BinaryCounterReading_getSequenceNumber(bcr));
    cJSON* q = cJSON_AddArrayToObject(o, "quality");
    if (BinaryCounterReading_hasCarry(bcr)) cJSON_AddItemToArray(q, cJSON_CreateString("CY"));
    if (BinaryCounterReading_isAdjusted(bcr)) cJSON_AddItemToArray(q, cJSON_CreateString("CA"));
    if (BinaryCounterReading_isInvalid(bcr)) cJSON_AddItemToArray(q, cJSON_CreateString("IV"));
}

/* Fills o from io. Returns false for a type the adapter does not model. */
static bool objectToJson(cJSON* o, InformationObject io, IEC60870_5_TypeID type)
{
    switch (type) {
    case M_SP_TB_1:
        addTime(o, SinglePointWithCP56Time2a_getTimestamp((SinglePointWithCP56Time2a)io));
        /* fall through */
    case M_SP_NA_1:
        cJSON_AddBoolToObject(o, "value", SinglePointInformation_getValue((SinglePointInformation)io));
        cJSON_AddItemToObject(o, "quality", qualityToJson(SinglePointInformation_getQuality((SinglePointInformation)io)));
        return true;
    case M_DP_TB_1:
        addTime(o, DoublePointWithCP56Time2a_getTimestamp((DoublePointWithCP56Time2a)io));
        /* fall through */
    case M_DP_NA_1:
        cJSON_AddNumberToObject(o, "value", DoublePointInformation_getValue((DoublePointInformation)io));
        cJSON_AddItemToObject(o, "quality", qualityToJson(DoublePointInformation_getQuality((DoublePointInformation)io)));
        return true;
    case M_ST_TB_1:
        addTime(o, StepPositionWithCP56Time2a_getTimestamp((StepPositionWithCP56Time2a)io));
        /* fall through */
    case M_ST_NA_1:
        cJSON_AddNumberToObject(o, "value", StepPositionInformation_getValue((StepPositionInformation)io));
        cJSON_AddBoolToObject(o, "transient", StepPositionInformation_isTransient((StepPositionInformation)io));
        cJSON_AddItemToObject(o, "quality", qualityToJson(StepPositionInformation_getQuality((StepPositionInformation)io)));
        return true;
    case M_BO_TB_1:
        addTime(o, Bitstring32WithCP56Time2a_getTimestamp((Bitstring32WithCP56Time2a)io));
        /* fall through */
    case M_BO_NA_1:
        cJSON_AddNumberToObject(o, "value", BitString32_getValue((BitString32)io));
        cJSON_AddItemToObject(o, "quality", qualityToJson(BitString32_getQuality((BitString32)io)));
        return true;
    case M_ME_TD_1:
        addTime(o, MeasuredValueNormalizedWithCP56Time2a_getTimestamp((MeasuredValueNormalizedWithCP56Time2a)io));
        /* fall through */
    case M_ME_NA_1:
        addNormalized(o, MeasuredValueNormalized_getValue((MeasuredValueNormalized)io));
        cJSON_AddItemToObject(o, "quality", qualityToJson(MeasuredValueNormalized_getQuality((MeasuredValueNormalized)io)));
        return true;
    case M_ME_TE_1:
        addTime(o, MeasuredValueScaledWithCP56Time2a_getTimestamp((MeasuredValueScaledWithCP56Time2a)io));
        /* fall through */
    case M_ME_NB_1:
        cJSON_AddNumberToObject(o, "value", MeasuredValueScaled_getValue((MeasuredValueScaled)io));
        cJSON_AddItemToObject(o, "quality", qualityToJson(MeasuredValueScaled_getQuality((MeasuredValueScaled)io)));
        return true;
    case M_ME_TF_1:
        addTime(o, MeasuredValueShortWithCP56Time2a_getTimestamp((MeasuredValueShortWithCP56Time2a)io));
        /* fall through */
    case M_ME_NC_1:
        cJSON_AddNumberToObject(o, "value", (double)MeasuredValueShort_getValue((MeasuredValueShort)io));
        cJSON_AddItemToObject(o, "quality", qualityToJson(MeasuredValueShort_getQuality((MeasuredValueShort)io)));
        return true;
    case M_IT_TB_1:
        addTime(o, IntegratedTotalsWithCP56Time2a_getTimestamp((IntegratedTotalsWithCP56Time2a)io));
        /* fall through */
    case M_IT_NA_1:
        addCounter(o, IntegratedTotals_getBCR((IntegratedTotals)io));
        return true;

    case C_SC_TA_1:
        addTime(o, SingleCommandWithCP56Time2a_getTimestamp((SingleCommandWithCP56Time2a)io));
        /* fall through */
    case C_SC_NA_1:
        cJSON_AddBoolToObject(o, "value", SingleCommand_getState((SingleCommand)io));
        addCommand(o, SingleCommand_isSelect((SingleCommand)io), SingleCommand_getQU((SingleCommand)io));
        return true;
    case C_DC_TA_1:
        addTime(o, DoubleCommandWithCP56Time2a_getTimestamp((DoubleCommandWithCP56Time2a)io));
        /* fall through */
    case C_DC_NA_1:
        cJSON_AddNumberToObject(o, "value", DoubleCommand_getState((DoubleCommand)io));
        addCommand(o, DoubleCommand_isSelect((DoubleCommand)io), DoubleCommand_getQU((DoubleCommand)io));
        return true;
    case C_RC_TA_1:
        addTime(o, StepCommandWithCP56Time2a_getTimestamp((StepCommandWithCP56Time2a)io));
        /* fall through */
    case C_RC_NA_1:
        cJSON_AddNumberToObject(o, "value", StepCommand_getState((StepCommand)io));
        addCommand(o, StepCommand_isSelect((StepCommand)io), StepCommand_getQU((StepCommand)io));
        return true;
    case C_SE_TA_1:
        addTime(o, SetpointCommandNormalizedWithCP56Time2a_getTimestamp((SetpointCommandNormalizedWithCP56Time2a)io));
        /* fall through */
    case C_SE_NA_1:
        addNormalized(o, SetpointCommandNormalized_getValue((SetpointCommandNormalized)io));
        addCommand(o, SetpointCommandNormalized_isSelect((SetpointCommandNormalized)io),
            SetpointCommandNormalized_getQL((SetpointCommandNormalized)io));
        return true;
    case C_SE_TB_1:
        addTime(o, SetpointCommandScaledWithCP56Time2a_getTimestamp((SetpointCommandScaledWithCP56Time2a)io));
        /* fall through */
    case C_SE_NB_1:
        cJSON_AddNumberToObject(o, "value", SetpointCommandScaled_getValue((SetpointCommandScaled)io));
        addCommand(o, SetpointCommandScaled_isSelect((SetpointCommandScaled)io),
            SetpointCommandScaled_getQL((SetpointCommandScaled)io));
        return true;
    case C_SE_TC_1:
        addTime(o, SetpointCommandShortWithCP56Time2a_getTimestamp((SetpointCommandShortWithCP56Time2a)io));
        /* fall through */
    case C_SE_NC_1:
        cJSON_AddNumberToObject(o, "value", (double)SetpointCommandShort_getValue((SetpointCommandShort)io));
        addCommand(o, SetpointCommandShort_isSelect((SetpointCommandShort)io),
            SetpointCommandShort_getQL((SetpointCommandShort)io));
        return true;

    case M_EI_NA_1:
        cJSON_AddNumberToObject(o, "coi", EndOfInitialization_getCOI((EndOfInitialization)io));
        return true;
    case C_IC_NA_1:
        cJSON_AddNumberToObject(o, "qoi", InterrogationCommand_getQOI((InterrogationCommand)io));
        return true;
    case C_CI_NA_1:
        cJSON_AddNumberToObject(o, "qcc", CounterInterrogationCommand_getQCC((CounterInterrogationCommand)io));
        return true;
    case C_RD_NA_1:
        return true;
    case C_CS_NA_1:
        addTime(o, ClockSynchronizationCommand_getTime((ClockSynchronizationCommand)io));
        return true;
    case C_TS_TA_1:
        cJSON_AddNumberToObject(o, "counter", TestCommandWithCP56Time2a_getCounter((TestCommandWithCP56Time2a)io));
        addTime(o, TestCommandWithCP56Time2a_getTimestamp((TestCommandWithCP56Time2a)io));
        return true;
    default:
        return false;
    }
}

cJSON* asduToJson(CS101_ASDU asdu)
{
    IEC60870_5_TypeID type = CS101_ASDU_getTypeID(asdu);
    cJSON* j = cJSON_CreateObject();
    const char* name = typeName(type);
    if (name != NULL)
        cJSON_AddStringToObject(j, "type", name);
    else
        cJSON_AddNullToObject(j, "type");
    cJSON_AddNumberToObject(j, "typeId", type);
    cJSON_AddNumberToObject(j, "cot", CS101_ASDU_getCOT(asdu));
    cJSON_AddBoolToObject(j, "negative", CS101_ASDU_isNegative(asdu));
    cJSON_AddBoolToObject(j, "test", CS101_ASDU_isTest(asdu));
    cJSON_AddNumberToObject(j, "originator", CS101_ASDU_getOA(asdu));
    cJSON_AddNumberToObject(j, "commonAddress", CS101_ASDU_getCA(asdu));
    cJSON_AddBoolToObject(j, "sequence", CS101_ASDU_isSequence(asdu));
    int n = CS101_ASDU_getNumberOfElements(asdu);
    cJSON_AddNumberToObject(j, "count", n);
    cJSON* objects = cJSON_AddArrayToObject(j, "objects");
    if (name == NULL)
        return j; /* a type this adapter does not model: header only */
    for (int i = 0; i < n; i++) {
        InformationObject io = CS101_ASDU_getElement(asdu, i);
        if (io == NULL)
            break;
        cJSON* o = cJSON_CreateObject();
        cJSON_AddNumberToObject(o, "ioa", InformationObject_getObjectAddress(io));
        objectToJson(o, io, type);
        cJSON_AddItemToArray(objects, o);
        InformationObject_destroy(io);
    }
    return j;
}
