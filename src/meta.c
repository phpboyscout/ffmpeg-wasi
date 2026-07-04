// ffmpeg-wasi — shared metadata glue: the AV_DISPOSITION_* name table and the
// tag/disposition ↔ JSON conversions used by both the probe (read) and process
// (write) paths (spec 0020). All native libavutil — no new dependency.
//
// SPDX-License-Identifier: MIT
// Copyright (c) 2026 Matt Cockayne

#include <stdio.h>
#include <string.h>

#include <libavformat/avformat.h> // AV_DISPOSITION_*

#include "meta.h"

// The disposition flags we name, both directions. A subset of AV_DISPOSITION_*
// covering the ones a consumer acts on; unknown/rare flags are simply not named.
static const struct {
    const char *name;
    int flag;
} DISPOSITIONS[] = {
    {"default", AV_DISPOSITION_DEFAULT},
    {"dub", AV_DISPOSITION_DUB},
    {"original", AV_DISPOSITION_ORIGINAL},
    {"comment", AV_DISPOSITION_COMMENT},
    {"lyrics", AV_DISPOSITION_LYRICS},
    {"karaoke", AV_DISPOSITION_KARAOKE},
    {"forced", AV_DISPOSITION_FORCED},
    {"hearing_impaired", AV_DISPOSITION_HEARING_IMPAIRED},
    {"visual_impaired", AV_DISPOSITION_VISUAL_IMPAIRED},
    {"clean_effects", AV_DISPOSITION_CLEAN_EFFECTS},
    {"attached_pic", AV_DISPOSITION_ATTACHED_PIC},
    {"captions", AV_DISPOSITION_CAPTIONS},
    {"descriptions", AV_DISPOSITION_DESCRIPTIONS},
    {"metadata", AV_DISPOSITION_METADATA},
    {"dependent", AV_DISPOSITION_DEPENDENT},
    {"still_image", AV_DISPOSITION_STILL_IMAGE},
};

void meta_add_tags(cJSON *parent, const AVDictionary *m) {
    if (!m || av_dict_count(m) == 0) return;

    cJSON *tags = cJSON_AddObjectToObject(parent, "tags");
    const AVDictionaryEntry *e = NULL;
    while ((e = av_dict_iterate(m, e))) {
        cJSON_AddStringToObject(tags, e->key, e->value);
    }
}

void meta_add_disposition(cJSON *parent, int disposition) {
    cJSON *arr = cJSON_AddArrayToObject(parent, "disposition");
    for (size_t i = 0; i < sizeof(DISPOSITIONS) / sizeof(DISPOSITIONS[0]); i++) {
        if (disposition & DISPOSITIONS[i].flag) {
            cJSON_AddItemToArray(arr, cJSON_CreateString(DISPOSITIONS[i].name));
        }
    }
}

void meta_apply_tags(AVDictionary **dst, const cJSON *tags) {
    if (!cJSON_IsObject(tags)) return;

    const cJSON *kv = NULL;
    cJSON_ArrayForEach(kv, tags) {
        if (cJSON_IsString(kv)) av_dict_set(dst, kv->string, kv->valuestring, 0);
    }
}

int meta_disposition_from_json(const cJSON *arr) {
    if (!cJSON_IsArray(arr)) return 0;

    int flags = 0;
    const cJSON *e = NULL;
    cJSON_ArrayForEach(e, arr) {
        const char *name = cJSON_GetStringValue(e);
        if (!name) continue;

        int matched = 0;
        for (size_t i = 0; i < sizeof(DISPOSITIONS) / sizeof(DISPOSITIONS[0]); i++) {
            if (strcmp(name, DISPOSITIONS[i].name) == 0) {
                flags |= DISPOSITIONS[i].flag;
                matched = 1;
                break;
            }
        }
        if (!matched) fprintf(stderr, "ffmpeg-wasi: unknown disposition flag %s (ignored)\n", name);
    }
    return flags;
}
