#import <Foundation/Foundation.h>
#import <CoreAudio/CoreAudio.h>
#import <CoreAudio/AudioHardwareTapping.h>
#import <CoreAudio/CATapDescription.h>
#include <math.h>
#include <unistd.h>

static OSStatus get(AudioObjectID id,AudioObjectPropertySelector selector,UInt32 *size,void *value){
 AudioObjectPropertyAddress a={selector,kAudioObjectPropertyScopeGlobal,kAudioObjectPropertyElementMain};
 return AudioObjectGetPropertyData(id,&a,0,NULL,size,value);
}
static uint64_t samples=0,nonzero=0;
static double peak=0,squares=0;
static OSStatus receive(AudioObjectID device,const AudioTimeStamp*now,const AudioBufferList*in,const AudioTimeStamp*inTime,AudioBufferList*out,const AudioTimeStamp*outTime,void*context){
 for(UInt32 j=0;j<in->mNumberBuffers;j++){
  const AudioBuffer*b=&in->mBuffers[j];const float*p=b->mData;
  if(!p)continue;
  for(UInt32 i=0;i<b->mDataByteSize/sizeof(float);i++){double x=p[i];if(!isfinite(x))continue;samples++;squares+=x*x;if(x!=0)nonzero++;if(fabs(x)>peak)peak=fabs(x);}
 }
 return noErr;
}
int main(int argc,char**argv){@autoreleasepool{
 if(argc!=2)return 2;
 pid_t pid=atoi(argv[1]);AudioObjectID process=0;
 AudioObjectPropertyAddress address={kAudioHardwarePropertyTranslatePIDToProcessObject,kAudioObjectPropertyScopeGlobal,kAudioObjectPropertyElementMain};
 UInt32 size=sizeof(process);
 OSStatus err=AudioObjectGetPropertyData(kAudioObjectSystemObject,&address,sizeof(pid),&pid,&size,&process);
 if(err||!process){fprintf(stderr,"No CoreAudio process for PID %d (%d)\n",pid,(int)err);return 1;}
 CATapDescription*d=[[CATapDescription alloc]initStereoMixdownOfProcesses:@[@(process)]];
 d.UUID=[NSUUID UUID];d.muteBehavior=CATapUnmuted;d.privateTap=YES;
 AudioObjectID tap=0,aggregate=0;AudioDeviceIOProcID proc=NULL;
 err=AudioHardwareCreateProcessTap(d,&tap);if(err){fprintf(stderr,"Tap failed %d\n",(int)err);return 1;}
 AudioDeviceID output=0;size=sizeof(output);get(kAudioObjectSystemObject,kAudioHardwarePropertyDefaultOutputDevice,&size,&output);
 CFStringRef uid=NULL;size=sizeof(uid);get(output,kAudioDevicePropertyDeviceUID,&size,&uid);
 NSDictionary*spec=@{@kAudioAggregateDeviceNameKey:@"Chill output measurement",@kAudioAggregateDeviceUIDKey:[[NSUUID UUID]UUIDString],@kAudioAggregateDeviceMainSubDeviceKey:(__bridge NSString*)uid,@kAudioAggregateDeviceIsPrivateKey:@YES,@kAudioAggregateDeviceIsStackedKey:@NO,@kAudioAggregateDeviceTapAutoStartKey:@YES,@kAudioAggregateDeviceSubDeviceListKey:@[@{@kAudioSubDeviceUIDKey:(__bridge NSString*)uid}],@kAudioAggregateDeviceTapListKey:@[@{@kAudioSubTapUIDKey:[d.UUID UUIDString],@kAudioSubTapDriftCompensationKey:@YES}]};
 err=AudioHardwareCreateAggregateDevice((__bridge CFDictionaryRef)spec,&aggregate);
 if(!err)err=AudioDeviceCreateIOProcID(aggregate,receive,NULL,&proc);
 if(!err)err=AudioDeviceStart(aggregate,proc);
 if(!err)sleep(2);
 if(proc){AudioDeviceStop(aggregate,proc);AudioDeviceDestroyIOProcID(aggregate,proc);}
 if(aggregate)AudioHardwareDestroyAggregateDevice(aggregate);
 AudioHardwareDestroyProcessTap(tap);
 if(uid)CFRelease(uid);
 printf("{\"pid\":%d,\"error\":%d,\"samples\":%llu,\"nonzero\":%llu,\"peak\":%.6f,\"rms\":%.6f}\n",pid,(int)err,(unsigned long long)samples,(unsigned long long)nonzero,peak,samples?sqrt(squares/samples):0);
 return err||nonzero==0?1:0;
}}
