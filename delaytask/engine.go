package delaytask

import (
	"time"
	"context"
	"sync"
	"github.com/sirupsen/logrus"
)

type DelayTaskEngine struct {
	timeWheel *TimeWheeler
	Storage   *TaskStorageService

	factory   Factory
	threshold time.Duration
	eventChan chan Event
	wg        sync.WaitGroup
	quit      chan bool
}

func (engine *DelayTaskEngine) createTask(task string) Runner {
	return engine.factory.Create(task)
}

func (engine *DelayTaskEngine) AddTaskCreator(name string, creator Creator) {
	engine.factory.Register(name, creator)
}

func (engine *DelayTaskEngine) LoadOngoingTask() {
	ongoingTask := &TaskLoadOngoing{}
	engine.eventChan <- ongoingTask
}

func (engine *DelayTaskEngine) EventOccur() {
	// 时间到通知，从waitingQ中获取
	e := &TaskLoadingEvent{}
	engine.eventChan <- e
}

func (engine *DelayTaskEngine) addTask(t string) bool {
	toRunAt, ok := engine.Storage.GetTaskToRunAt(t)
	//toRunAtTime := time.Time(toRunAt*1e9)
	toRunAtTime := time.Unix(toRunAt, 0)
	thresholdTime := time.Now().Add(engine.threshold)
	if ok {
		if toRunAtTime.After(thresholdTime) {
			// todo 要保证顺序，添加要发生在 change complete 前面，csp
			go engine.Storage.AddWaitingTask(t)
		} else {
			// todo 要保证顺序，添加要发生在 change complete 前面，csp
			go engine.Storage.AddOngoingTask(t)
			task := engine.createTask(t)
			engine.timeWheel.Add(task)
		}
		return true
	} else {
		return false
	}
}

func (engine *DelayTaskEngine) add(t string) {
	engine.addTask(t)
}
func (engine *DelayTaskEngine) remove(taskID string) {
	// todo 要保证顺序 change complete 在添加之后，使用同一个goroutine
	go engine.Storage.ChangeTaskToComplete(taskID)
}

func (engine *DelayTaskEngine) Start() {
	/*
	1.创建timewheel,NewTimeWheel
	2.创建timeservice,
	3.创建storage service，添加回调，自动取出新发布的task，如果已经< threshold 发布到ongoingQ并添加到timewheel
		如果> threshold则放入waitingQ，
	4.创建创建定时器回调，定时从waitingQ中获取task放入ongoingQ，并且添加到timewheel中
	5.task 触发运行，触发回调函数把task从ongoing中移除；
	*/
	engine.timeWheel.Start()
	engine.Storage.Start()
	TimerService.Start()
	go func() {
		engine.wg.Add(1)
	loop:
		for {
			select {
			case event := <-engine.eventChan:
				taskType := event.GetType()
				switch taskType {
				case TaskCompleteEventType:
					engine.remove(event.GetBody())
				case TaskAddEventType:
					engine.add(event.GetBody())
				case TaskReceivedEventType:
					engine.onMessage(event.GetBody())
				case TaskLoadingOngoingsEventType:
					taskStr, err := engine.Storage.LoadOngoingTask()
					if err != nil {
						Logger.WithFields(logrus.Fields{
							"task": taskStr,
							"err":  err,
						}).Errorln("load ongoing task err")
					} else {
						for _, ts := range taskStr {
							task := engine.createTask(ts)
							if task != nil {
								engine.timeWheel.Add(task)
							} else {
								Logger.WithFields(logrus.Fields{
									"taskStr": taskStr,
									"task":    task,
								}).Errorln("DelayTaskEngine start:ASK_LOAD_ONGOING:create task error")
							}
						}
					}
				case PeriodTaskLoadingEventType:
					taskStr, err := engine.Storage.MoveWaitingToOngoingQ(engine.threshold)
					if err != nil {
					} else {
						for _, ts := range taskStr {
							task := engine.createTask(ts)
							if task != nil {
								engine.timeWheel.Add(task)
							} else {
							}
						}
					}
				default:
				}
			case <-engine.quit:
				break loop
			}
		}
		engine.wg.Done()
	}()
	// engine 启动之后 载入ongoing task
	engine.LoadOngoingTask()
}

func (engine *DelayTaskEngine) Stop() {
	TimerService.Stop()
	engine.Storage.Stop()
	engine.timeWheel.Stop()
	engine.quit <- true
	engine.wg.Wait()
}

func (engine *DelayTaskEngine) HandleEvent(event Event) {
	engine.eventChan <- event
}

func (engine *DelayTaskEngine) onMessage(message string) bool {
	return engine.addTask(message)
}

func NewEngine(duration string, slot int, subscribeUrl string, subscribeTopic string,
	prefix string) *DelayTaskEngine {

	tw :=NewTimeWheel(duration, slot)

	ctx, _ := context.WithCancel(context.Background())
	s := NewTaskStorageService(ctx, subscribeUrl, subscribeTopic, prefix)
	// 3 倍的
	dur := time.Duration(int64(tw.RoundDuration()) * 3)

	engine := &DelayTaskEngine{
		timeWheel: tw,
		Storage:   s,
		factory:   NewTaskFactory(),
		threshold: dur,
		eventChan: make(chan Event, 5),
		wg:        sync.WaitGroup{},
		quit:      make(chan bool),
	}
	// 每一个round，取一次待执行的任务，保证每次取回来的任务round都是2*round time -- 3* round time
	TimerService.GetTimer(tw.RoundDuration().String()).Register(engine)
	Tracker.Subscribe(TaskAddEventType, engine)
	Tracker.Subscribe(TaskCompleteEventType, engine)
	Tracker.Subscribe(TaskReceivedEventType, engine)

	return engine
}
