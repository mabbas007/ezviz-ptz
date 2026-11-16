/*
 * Fake Hikvision SDK used by the integration test.
 *
 * The real SDK only runs against a camera, so for tests we LD_PRELOAD this
 * shared object over libhcnetsdk.so.  It accepts every command and reports a
 * fixed PTZ position plus a fixed encoder configuration so the whole ONVIF
 * surface can be exercised offline.
 *
 * Build with: gcc -shared -fPIC -o libfakesdk.so fake_hcnetsdk.c
 */
#include <string.h>

typedef unsigned short WORD;
typedef unsigned int DWORD;
typedef unsigned char BYTE;
typedef int LONG;
typedef int BOOL;

#define NET_DVR_GET_COMPRESSCFG_V30 1040

/* NET_DVR_COMPRESSION_INFO_V30, layout from HCNetSDK.h. */
typedef struct {
    BYTE byStreamType;
    BYTE byResolution;
    BYTE byBitrateType;
    BYTE byPicQuality;
    DWORD dwVideoBitrate;
    DWORD dwVideoFrameRate;
    WORD wIntervalFrameI;
    BYTE byIntervalBPFrame;
    BYTE byres1;
    BYTE byVideoEncType;
    BYTE byAudioEncType;
    BYTE byVideoEncComplexity;
    BYTE byEnableSvc;
    BYTE byFormatType;
    BYTE byAudioBitRate;
    BYTE byStreamSmooth;
    BYTE byAudioSamplingRate;
    BYTE bySmartCodec;
    BYTE byDepthMapEnable;
    WORD wAverageVideoBitrate;
} fake_compression_info_v30;

/* NET_DVR_COMPRESSIONCFG_V30, layout from HCNetSDK.h. */
typedef struct {
    DWORD dwSize;
    fake_compression_info_v30 struNormHighRecordPara;
    fake_compression_info_v30 struRes;
    fake_compression_info_v30 struEventRecordPara;
    fake_compression_info_v30 struNetPara;
} fake_compressioncfg_v30;

int NET_DVR_Init(void) { return 1; }
int NET_DVR_Cleanup(void) { return 1; }
unsigned int NET_DVR_GetLastError(void) { return 0; }

LONG NET_DVR_Login_V30(char *ip, WORD port, char *user, char *pass, void *di) {
    (void)ip;
    (void)port;
    (void)user;
    (void)pass;
    if (di) {
        /* NET_DVR_DEVICEINFO_V30.byStartChan / byStartDChan, see HCNetSDK.h */
        ((unsigned char *)di)[564] = 1;
    }
    return 0;
}

BOOL NET_DVR_Logout(LONG uid) {
    (void)uid;
    return 1;
}

int NET_DVR_PTZControl_Other(LONG uid, LONG chan, DWORD cmd, DWORD stop) {
    (void)uid;
    (void)chan;
    (void)cmd;
    (void)stop;
    return 1;
}

int NET_DVR_PTZControlWithSpeed_Other(
    LONG uid, LONG chan, DWORD cmd, DWORD stop, DWORD speed
) {
    (void)uid;
    (void)chan;
    (void)cmd;
    (void)stop;
    (void)speed;
    return 1;
}

int NET_DVR_GetDVRConfig(
    LONG uid, DWORD cmd, LONG chan, void *buf, DWORD size, DWORD *ret
) {
    (void)uid;
    (void)chan;
    (void)size;
    if (ret) {
        *ret = 0;
    }

    if (cmd == NET_DVR_GET_COMPRESSCFG_V30) {
        /* Main/network stream: H.265, 1920x1080, 30 fps, 4096 kbps. */
        fake_compressioncfg_v30 *cfg = (fake_compressioncfg_v30 *)buf;
        memset(cfg, 0, sizeof(*cfg));
        cfg->dwSize = sizeof(*cfg);
        cfg->struNetPara.byResolution = 39;
        cfg->struNetPara.byVideoEncType = 10;
        cfg->struNetPara.dwVideoFrameRate = 18;
        cfg->struNetPara.dwVideoBitrate = 0x80000000u | 4096u;
        cfg->struNetPara.wAverageVideoBitrate = 4096;
        return 1;
    }

    /* NET_DVR_PTZPOS: wAction, wPanPos, wTiltPos, wZoomPos */
    unsigned short *p = (unsigned short *)buf;
    p[0] = 0;
    p[1] = 1234;
    p[2] = 5678;
    p[3] = 90;
    return 1;
}
