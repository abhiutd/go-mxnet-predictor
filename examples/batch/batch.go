package main

// #include <cuda.h>
// #include <cuda_runtime.h>
// #include <cuda_profiler_api.h>
// #include <cudaProfiler.h>
import "C"

import (
	"bufio"
	"context"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"

	"github.com/rai-project/dlframework/framework/options"
	cupti "github.com/rai-project/go-cupti"
	nvidiasmi "github.com/rai-project/nvidia-smi"

	"github.com/k0kubun/pp"
	"github.com/rai-project/go-mxnet-predictor/mxnet"

	"github.com/anthonynsimon/bild/imgio"
	"github.com/anthonynsimon/bild/transform"
	"github.com/rai-project/config"
	"github.com/rai-project/downloadmanager"
	"github.com/rai-project/go-mxnet-predictor/utils"
	"github.com/rai-project/tracer"
	_ "github.com/rai-project/tracer/all"
)

var (
	batch        = 10
	graph_url    = "http://data.dmlc.ml/models/imagenet/squeezenet/squeezenet_v1.0-symbol.json"
	weights_url  = "http://data.dmlc.ml/models/imagenet/squeezenet/squeezenet_v1.0-0000.params"
	features_url = "http://data.dmlc.ml/mxnet/models/imagenet/synset.txt"
)

func main() {

	defer func() {
		tracer.Close()
	}()

	dir, _ := filepath.Abs("../tmp")
	graph := filepath.Join(dir, "squeezenet_v1.0-symbol.json")
	weights := filepath.Join(dir, "squeezenet_v1.0-0000.params")
	features := filepath.Join(dir, "synset.txt")

	if _, err := downloadmanager.DownloadInto(graph_url, dir); err != nil {
		os.Exit(-1)
	}

	if _, err := downloadmanager.DownloadInto(weights_url, dir); err != nil {
		os.Exit(-1)
	}

	if _, err := downloadmanager.DownloadInto(features_url, dir); err != nil {
		os.Exit(-1)
	}

	// load model
	symbol, err := ioutil.ReadFile(graph)
	if err != nil {
		panic(err)
	}
	params, err := ioutil.ReadFile(weights)
	if err != nil {
		panic(err)
	}

	var input []float32
	cnt := 0

	imgDir, _ := filepath.Abs("../_fixtures")
	err = filepath.Walk(imgDir, func(path string, info os.FileInfo, err error) error {
		if path == imgDir || filepath.Ext(path) != ".jpg" || cnt >= batch {
			return nil
		}

		img, err := imgio.Open(path)
		if err != nil {
			return err
		}
		resized := transform.Resize(img, 224, 224, transform.Linear)
		res, err := utils.CvtImageTo1DArray(resized)
		if err != nil {
			panic(err)
		}
		input = append(input, res...)
		cnt++

		return nil
	})
	if err != nil {
		panic(err)
	}

	opts := options.New()
	inputDims := []uint32{3, 224, 224}

	device := options.CPU_DEVICE
	if nvidiasmi.HasGPU {
		device = options.CUDA_DEVICE
	}
	pp.Println("Using device = ", device)

	span, ctx := tracer.StartSpanFromContext(context.Background(), tracer.FULL_TRACE, "mxnet_batch")
	defer span.Finish()

	// create predictor
	p, err := mxnet.CreatePredictor(
		options.WithOptions(opts),
		options.Device(device, 0),
		options.Symbol(symbol),
		options.Weights(params),
		options.InputNode("data", inputDims),
		options.BatchSize(uint32(batch)),
	)

	if err != nil {
		panic(err)
	}
	defer p.Close()

	// set input
	if err := p.SetInput("data", input); err != nil {
		panic(err)
	}

	if nvidiasmi.HasGPU {
		cu, err := cupti.New(cupti.Context(ctx))
		if err == nil {
			defer func() {
				cu.Wait()
				cu.Close()
			}()
		}
	}

	if profile, err := mxnet.NewProfile(mxnet.ProfileAllOperators, ""); err == nil {
		profile.Start()

		defer func() {
			profile.Stop()

			profile.Publish(ctx)
			profile.Delete()
		}()
	}

	// do predict
	if err := p.Forward(); err != nil {
		panic(err)
	}

	// get predict result
	output, err := p.GetOutput(0)
	if err != nil {
		panic(err)
	}

	var labels []string
	f, err := os.Open(features)
	if err != nil {
		os.Exit(-1)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		labels = append(labels, line)
	}

	len := len(output) / batch
	for i := 0; i < cnt; i++ {
		idxs := make([]int, len)
		for j := 0; j < len; j++ {
			idxs[j] = j
		}
		as := utils.ArgSort{Args: output[i*len : (i+1)*len], Idxs: idxs}
		sort.Sort(as)

		pp.Println(as.Args[0])
		pp.Println(labels[as.Idxs[0]])
	}

	// os.RemoveAll(dir)
}

func init() {
	config.Init(
		config.AppName("carml"),
		config.VerboseMode(true),
		config.DebugMode(true),
	)
}
