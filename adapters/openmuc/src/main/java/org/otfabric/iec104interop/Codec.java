// SPDX-License-Identifier: GPL-3.0-or-later
package org.otfabric.iec104interop;

import java.time.Instant;
import java.time.ZoneOffset;
import java.time.format.DateTimeFormatter;
import java.util.EnumSet;
import java.util.Set;
import java.util.TimeZone;

import org.openmuc.j60870.ASdu;
import org.openmuc.j60870.ASduType;
import org.openmuc.j60870.ie.IeBinaryCounterReading;
import org.openmuc.j60870.ie.IeBinaryStateInformation;
import org.openmuc.j60870.ie.IeCauseOfInitialization;
import org.openmuc.j60870.ie.IeDoubleCommand;
import org.openmuc.j60870.ie.IeDoublePointWithQuality;
import org.openmuc.j60870.ie.IeDoublePointWithQuality.DoublePointInformation;
import org.openmuc.j60870.ie.IeNormalizedValue;
import org.openmuc.j60870.ie.IeQualifierOfCounterInterrogation;
import org.openmuc.j60870.ie.IeQualifierOfInterrogation;
import org.openmuc.j60870.ie.IeQualifierOfSetPointCommand;
import org.openmuc.j60870.ie.IeQuality;
import org.openmuc.j60870.ie.IeRegulatingStepCommand;
import org.openmuc.j60870.ie.IeScaledValue;
import org.openmuc.j60870.ie.IeShortFloat;
import org.openmuc.j60870.ie.IeSingleCommand;
import org.openmuc.j60870.ie.IeSinglePointWithQuality;
import org.openmuc.j60870.ie.IeTestSequenceCounter;
import org.openmuc.j60870.ie.IeTime56;
import org.openmuc.j60870.ie.IeValueWithTransientState;
import org.openmuc.j60870.ie.InformationElement;
import org.openmuc.j60870.ie.InformationObject;

import com.google.gson.JsonArray;
import com.google.gson.JsonNull;
import com.google.gson.JsonObject;

/** Conversions between the fixture model, j60870 objects and the JSON of the contract. */
final class Codec {
    static final TimeZone UTC = TimeZone.getTimeZone("UTC");
    private static final DateTimeFormatter TIME = DateTimeFormatter.ofPattern("yyyy-MM-dd'T'HH:mm:ss.SSS'Z'")
            .withZone(ZoneOffset.UTC);

    /** The types this adapter models, by the names of the contract. */
    private static final Set<ASduType> MODELLED = EnumSet.of(
            ASduType.M_SP_NA_1, ASduType.M_DP_NA_1, ASduType.M_ST_NA_1, ASduType.M_BO_NA_1, ASduType.M_ME_NA_1,
            ASduType.M_ME_NB_1, ASduType.M_ME_NC_1, ASduType.M_IT_NA_1, ASduType.M_SP_TB_1, ASduType.M_DP_TB_1,
            ASduType.M_ST_TB_1, ASduType.M_BO_TB_1, ASduType.M_ME_TD_1, ASduType.M_ME_TE_1, ASduType.M_ME_TF_1,
            ASduType.M_IT_TB_1, ASduType.C_SC_NA_1, ASduType.C_DC_NA_1, ASduType.C_RC_NA_1, ASduType.C_SE_NA_1,
            ASduType.C_SE_NB_1, ASduType.C_SE_NC_1, ASduType.C_SC_TA_1, ASduType.C_DC_TA_1, ASduType.C_RC_TA_1,
            ASduType.C_SE_TA_1, ASduType.C_SE_TB_1, ASduType.C_SE_TC_1, ASduType.M_EI_NA_1, ASduType.C_IC_NA_1,
            ASduType.C_CI_NA_1, ASduType.C_RD_NA_1, ASduType.C_CS_NA_1, ASduType.C_TS_TA_1);

    private Codec() {
    }

    static String formatTime(long ms) {
        return TIME.format(Instant.ofEpochMilli(ms));
    }

    static IeTime56 time(long ms) {
        return new IeTime56(ms, UTC, false);
    }

    /** Maps a command type with CP56Time2a to the one without; other types map to themselves. */
    static ASduType baseType(ASduType t) {
        switch (t) {
        case C_SC_TA_1:
            return ASduType.C_SC_NA_1;
        case C_DC_TA_1:
            return ASduType.C_DC_NA_1;
        case C_RC_TA_1:
            return ASduType.C_RC_NA_1;
        case C_SE_TA_1:
            return ASduType.C_SE_NA_1;
        case C_SE_TB_1:
            return ASduType.C_SE_NB_1;
        case C_SE_TC_1:
            return ASduType.C_SE_NC_1;
        default:
            return t;
        }
    }

    /** The CP56Time2a variant of a point or command type. */
    static ASduType timeTagged(ASduType t) {
        switch (t) {
        case M_SP_NA_1:
            return ASduType.M_SP_TB_1;
        case M_DP_NA_1:
            return ASduType.M_DP_TB_1;
        case M_ST_NA_1:
            return ASduType.M_ST_TB_1;
        case M_BO_NA_1:
            return ASduType.M_BO_TB_1;
        case M_ME_NA_1:
            return ASduType.M_ME_TD_1;
        case M_ME_NB_1:
            return ASduType.M_ME_TE_1;
        case M_ME_NC_1:
            return ASduType.M_ME_TF_1;
        case M_IT_NA_1:
            return ASduType.M_IT_TB_1;
        case C_SC_NA_1:
            return ASduType.C_SC_TA_1;
        case C_DC_NA_1:
            return ASduType.C_DC_TA_1;
        case C_RC_NA_1:
            return ASduType.C_RC_TA_1;
        case C_SE_NA_1:
            return ASduType.C_SE_TA_1;
        case C_SE_NB_1:
            return ASduType.C_SE_TB_1;
        case C_SE_NC_1:
            return ASduType.C_SE_TC_1;
        default:
            throw new IllegalArgumentException("no time-tagged variant of " + t);
        }
    }

    private static IeQuality quality(Set<String> q) {
        return new IeQuality(q.contains("OV"), q.contains("BL"), q.contains("SB"), q.contains("NT"), q.contains("IV"));
    }

    /** The information object for a point; timeMs null selects the type without time tag. */
    static InformationObject pointObject(Fixture.Point p, Long timeMs) {
        Set<String> q = p.quality;
        InformationElement[] e;
        switch (p.type) {
        case M_SP_NA_1:
            e = new InformationElement[] { new IeSinglePointWithQuality(p.value != 0, q.contains("BL"),
                    q.contains("SB"), q.contains("NT"), q.contains("IV")) };
            break;
        case M_DP_NA_1:
            e = new InformationElement[] { new IeDoublePointWithQuality(DoublePointInformation.values()[(int) p.value],
                    q.contains("BL"), q.contains("SB"), q.contains("NT"), q.contains("IV")) };
            break;
        case M_ST_NA_1:
            e = new InformationElement[] { new IeValueWithTransientState((int) p.value, p.transientState),
                    quality(q) };
            break;
        case M_BO_NA_1:
            e = new InformationElement[] { new IeBinaryStateInformation((int) (long) p.value), quality(q) };
            break;
        case M_ME_NA_1:
            e = new InformationElement[] { new IeNormalizedValue((int) p.value), quality(q) };
            break;
        case M_ME_NB_1:
            e = new InformationElement[] { new IeScaledValue((int) p.value), quality(q) };
            break;
        case M_ME_NC_1:
            e = new InformationElement[] { new IeShortFloat((float) p.value), quality(q) };
            break;
        case M_IT_NA_1: {
            Set<IeBinaryCounterReading.Flag> flags = EnumSet.noneOf(IeBinaryCounterReading.Flag.class);
            if (q.contains("CY")) {
                flags.add(IeBinaryCounterReading.Flag.CARRY);
            }
            if (q.contains("CA")) {
                flags.add(IeBinaryCounterReading.Flag.COUNTER_ADJUSTED);
            }
            if (q.contains("IV")) {
                flags.add(IeBinaryCounterReading.Flag.INVALID);
            }
            e = new InformationElement[] { new IeBinaryCounterReading((int) p.value, p.sequence, flags) };
            break;
        }
        default:
            throw new IllegalArgumentException("not a point type: " + p.type);
        }
        if (timeMs != null) {
            InformationElement[] withTime = new InformationElement[e.length + 1];
            System.arraycopy(e, 0, withTime, 0, e.length);
            withTime[e.length] = time(timeMs);
            e = withTime;
        }
        return new InformationObject(p.ioa, e);
    }

    private static JsonArray qualityJson(boolean ov, boolean bl, boolean sb, boolean nt, boolean iv) {
        JsonArray a = new JsonArray();
        if (ov) {
            a.add("OV");
        }
        if (bl) {
            a.add("BL");
        }
        if (sb) {
            a.add("SB");
        }
        if (nt) {
            a.add("NT");
        }
        if (iv) {
            a.add("IV");
        }
        return a;
    }

    private static JsonArray qualityJson(IeQuality q) {
        return qualityJson(q.isOverflow(), q.isBlocked(), q.isSubstituted(), q.isNotTopical(), q.isInvalid());
    }

    private static void addTime(JsonObject o, InformationElement e) {
        IeTime56 t = (IeTime56) e;
        o.addProperty("time", formatTime(t.getTimestamp(1970, UTC)));
        if (t.isInvalid()) {
            o.addProperty("timeInvalid", true);
        }
    }

    private static void addNormalized(JsonObject o, IeNormalizedValue v) {
        o.addProperty("value", v.getUnnormalizedValue());
        o.addProperty("normalized", v.getUnnormalizedValue() / 32768.0);
    }

    private static void addSetpointQualifier(JsonObject o, InformationElement e) {
        IeQualifierOfSetPointCommand q = (IeQualifierOfSetPointCommand) e;
        o.addProperty("select", q.isSelect());
        o.addProperty("qualifier", q.getQl());
    }

    /** Fills o from the elements of one object of the given type. */
    private static void elementsJson(JsonObject o, ASduType type, InformationElement[] e) {
        ASduType base = type;
        boolean timed = false;
        switch (type) {
        case M_SP_TB_1:
            base = ASduType.M_SP_NA_1;
            timed = true;
            break;
        case M_DP_TB_1:
            base = ASduType.M_DP_NA_1;
            timed = true;
            break;
        case M_ST_TB_1:
            base = ASduType.M_ST_NA_1;
            timed = true;
            break;
        case M_BO_TB_1:
            base = ASduType.M_BO_NA_1;
            timed = true;
            break;
        case M_ME_TD_1:
            base = ASduType.M_ME_NA_1;
            timed = true;
            break;
        case M_ME_TE_1:
            base = ASduType.M_ME_NB_1;
            timed = true;
            break;
        case M_ME_TF_1:
            base = ASduType.M_ME_NC_1;
            timed = true;
            break;
        case M_IT_TB_1:
            base = ASduType.M_IT_NA_1;
            timed = true;
            break;
        case C_SC_TA_1:
        case C_DC_TA_1:
        case C_RC_TA_1:
        case C_SE_TA_1:
        case C_SE_TB_1:
        case C_SE_TC_1:
            base = baseType(type);
            timed = true;
            break;
        default:
            break;
        }
        if (timed) {
            addTime(o, e[e.length - 1]);
        }
        switch (base) {
        case M_SP_NA_1: {
            IeSinglePointWithQuality v = (IeSinglePointWithQuality) e[0];
            o.addProperty("value", v.isOn());
            o.add("quality", qualityJson(false, v.isBlocked(), v.isSubstituted(), v.isNotTopical(), v.isInvalid()));
            break;
        }
        case M_DP_NA_1: {
            IeDoublePointWithQuality v = (IeDoublePointWithQuality) e[0];
            o.addProperty("value", v.getDoublePointInformation().ordinal());
            o.add("quality", qualityJson(false, v.isBlocked(), v.isSubstituted(), v.isNotTopical(), v.isInvalid()));
            break;
        }
        case M_ST_NA_1: {
            IeValueWithTransientState v = (IeValueWithTransientState) e[0];
            o.addProperty("value", v.getValue());
            o.addProperty("transient", v.getTransientState());
            o.add("quality", qualityJson((IeQuality) e[1]));
            break;
        }
        case M_BO_NA_1:
            o.addProperty("value", ((IeBinaryStateInformation) e[0]).getValue() & 0xFFFFFFFFL);
            o.add("quality", qualityJson((IeQuality) e[1]));
            break;
        case M_ME_NA_1:
            addNormalized(o, (IeNormalizedValue) e[0]);
            o.add("quality", qualityJson((IeQuality) e[1]));
            break;
        case M_ME_NB_1:
            o.addProperty("value", ((IeScaledValue) e[0]).getUnnormalizedValue());
            o.add("quality", qualityJson((IeQuality) e[1]));
            break;
        case M_ME_NC_1:
            o.addProperty("value", (double) ((IeShortFloat) e[0]).getValue());
            o.add("quality", qualityJson((IeQuality) e[1]));
            break;
        case M_IT_NA_1: {
            IeBinaryCounterReading v = (IeBinaryCounterReading) e[0];
            o.addProperty("value", v.getCounterReading());
            o.addProperty("sequence", v.getSequenceNumber());
            JsonArray q = new JsonArray();
            if (v.getFlags().contains(IeBinaryCounterReading.Flag.CARRY)) {
                q.add("CY");
            }
            if (v.getFlags().contains(IeBinaryCounterReading.Flag.COUNTER_ADJUSTED)) {
                q.add("CA");
            }
            if (v.getFlags().contains(IeBinaryCounterReading.Flag.INVALID)) {
                q.add("IV");
            }
            o.add("quality", q);
            break;
        }
        case C_SC_NA_1: {
            IeSingleCommand v = (IeSingleCommand) e[0];
            o.addProperty("value", v.isCommandStateOn());
            o.addProperty("select", v.isSelect());
            o.addProperty("qualifier", v.getQualifier());
            break;
        }
        case C_DC_NA_1: {
            IeDoubleCommand v = (IeDoubleCommand) e[0];
            o.addProperty("value", v.getCommandState().getId());
            o.addProperty("select", v.isSelect());
            o.addProperty("qualifier", v.getQualifier());
            break;
        }
        case C_RC_NA_1: {
            IeRegulatingStepCommand v = (IeRegulatingStepCommand) e[0];
            o.addProperty("value", v.getCommandState().getId());
            o.addProperty("select", v.isSelect());
            o.addProperty("qualifier", v.getQualifier());
            break;
        }
        case C_SE_NA_1:
            addNormalized(o, (IeNormalizedValue) e[0]);
            addSetpointQualifier(o, e[1]);
            break;
        case C_SE_NB_1:
            o.addProperty("value", ((IeScaledValue) e[0]).getUnnormalizedValue());
            addSetpointQualifier(o, e[1]);
            break;
        case C_SE_NC_1:
            o.addProperty("value", (double) ((IeShortFloat) e[0]).getValue());
            addSetpointQualifier(o, e[1]);
            break;
        case M_EI_NA_1:
            o.addProperty("coi", ((IeCauseOfInitialization) e[0]).getValue());
            break;
        case C_IC_NA_1:
            o.addProperty("qoi", ((IeQualifierOfInterrogation) e[0]).getValue());
            break;
        case C_CI_NA_1: {
            IeQualifierOfCounterInterrogation q = (IeQualifierOfCounterInterrogation) e[0];
            o.addProperty("qcc", q.getRequest() | q.getFreeze() << 6);
            break;
        }
        case C_RD_NA_1:
            break;
        case C_CS_NA_1:
            addTime(o, e[0]);
            break;
        case C_TS_TA_1:
            o.addProperty("counter", ((IeTestSequenceCounter) e[0]).getValue());
            addTime(o, e[1]);
            break;
        default:
            break;
        }
    }

    static JsonObject asduJson(ASdu a) {
        ASduType type = a.getTypeIdentification();
        boolean modelled = type != null && MODELLED.contains(type);
        JsonObject j = new JsonObject();
        if (modelled) {
            j.addProperty("type", type.name());
        } else {
            j.add("type", JsonNull.INSTANCE);
        }
        j.addProperty("typeId", type == null ? 0 : type.getId());
        j.addProperty("cot", a.getCauseOfTransmission().getId());
        j.addProperty("negative", a.isNegativeConfirm());
        j.addProperty("test", a.isTestFrame());
        Integer oa = a.getOriginatorAddress();
        j.addProperty("originator", oa == null ? 0 : oa);
        j.addProperty("commonAddress", a.getCommonAddress());
        j.addProperty("sequence", a.isSequenceOfElements());
        JsonArray objects = new JsonArray();
        int count = 0;
        InformationObject[] ios = a.getInformationObjects();
        if (ios != null) {
            for (InformationObject io : ios) {
                InformationElement[][] rows = io.getInformationElements();
                if (rows.length == 0) {
                    // j60870 decodes an object without elements (C_RD_NA_1) to no rows at all.
                    rows = new InformationElement[][] { {} };
                }
                // With SQ=1 one object carries a row of elements per consecutive address.
                for (int i = 0; i < rows.length; i++) {
                    count++;
                    if (!modelled) {
                        continue;
                    }
                    JsonObject o = new JsonObject();
                    o.addProperty("ioa", io.getInformationObjectAddress() + i);
                    elementsJson(o, type, rows[i]);
                    objects.add(o);
                }
            }
        }
        j.addProperty("count", count);
        j.add("objects", objects);
        return j;
    }
}
